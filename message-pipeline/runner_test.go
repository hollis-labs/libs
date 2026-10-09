package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStore struct {
	mu   sync.Mutex
	rows map[Key]Record
	err  error
}

func (s *memoryStore) Get(_ context.Context, k Key) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[k]
	return r.Clone(), ok, s.err
}
func (s *memoryStore) PutIfAbsent(_ context.Context, k Key, r Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return Record{}, s.err
	}
	if old, ok := s.rows[k]; ok {
		return old.Clone(), nil
	}
	if s.rows == nil {
		s.rows = make(map[Key]Record)
	}
	s.rows[k] = r.Clone()
	return r.Clone(), nil
}
func spec(id string, f StageFunc) StageSpec {
	return StageSpec{ID: id, Version: "1", ConfigDigest: "config1", InstructionDigest: "instruction1", Timeout: time.Second, Stage: f}
}
func message() Message {
	return Message{Identity: Identity{Source: "endpoint/channel", Message: "publication1"}, Original: "original 🔒"}
}
func runner(t *testing.T, specs []StageSpec, store ResultStore) *Runner {
	t.Helper()
	r, err := New(specs, store, Options{MaxOriginalBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestOrderingAccumulationAndIsolation(t *testing.T) {
	var order []string
	first := spec("first", func(_ context.Context, in Input) (Result, error) {
		order = append(order, "first")
		in.Message.Original = "changed"
		return Result{Summaries: []Summary{{Text: "one"}}}, nil
	})
	first.Priority = -1
	second := spec("second", func(_ context.Context, in Input) (Result, error) {
		order = append(order, "second")
		if in.Message.Original != message().Original || in.State.Annotations[0].Summary.Text != "one" {
			t.Error("input changed")
		}
		in.State.Annotations[0].Summary.Text = "corrupt"
		return Result{Summaries: []Summary{{Text: "two"}}}, nil
	})
	third := spec("third", func(_ context.Context, in Input) (Result, error) {
		order = append(order, "third")
		if in.State.Annotations[0].Summary.Text != "one" || len(in.State.Annotations) != 2 {
			t.Error("accumulation changed")
		}
		return Result{}, nil
	})
	r := runner(t, []StageSpec{second, first, third}, &memoryStore{})
	got, err := r.Run(context.Background(), message(), State{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "first,second,third" || got.Message.Original != message().Original || got.Disposition != Pass || got.State.Annotations[0].Summary.Text != "one" {
		t.Fatalf("wrong result: %+v %v", got, order)
	}
}
func TestFailuresPersistAndReplay(t *testing.T) {
	cases := []struct {
		name string
		f    StageFunc
		code FailureCode
	}{
		{"error", func(context.Context, Input) (Result, error) { return Result{}, errors.New("private provider error") }, StageError},
		{"panic", func(context.Context, Input) (Result, error) { panic("private panic") }, StagePanic},
		{"refused", func(context.Context, Input) (Result, error) { return Result{}, ErrRefused }, StageRefused},
		{"invalid", func(context.Context, Input) (Result, error) { return Result{Summaries: []Summary{{Text: ""}}}, nil }, InvalidOutput},
		{"hold", func(context.Context, Input) (Result, error) { return Result{Disposition: Hold}, nil }, InvalidOutput},
		{"followup", func(context.Context, Input) (Result, error) {
			return Result{FollowUps: []FollowUp{{Kind: "action"}}}, nil
		}, InvalidOutput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			s := spec(tc.name, func(ctx context.Context, in Input) (Result, error) { calls++; return tc.f(ctx, in) })
			r := runner(t, []StageSpec{s}, &memoryStore{})
			for range 2 {
				got, err := r.Run(context.Background(), message(), State{})
				if err != nil {
					t.Fatal(err)
				}
				if got.Disposition != Pass || got.State.Traces[0].FailureCode != tc.code || got.State.Traces[0].Outcome != Failed || len(got.State.Annotations) != 0 {
					t.Fatalf("wrong failure %+v", got)
				}
			}
			if calls != 1 {
				t.Fatalf("repeated spend %d", calls)
			}
		})
	}
}
func TestTimeoutLateResultAndBoundedWorkers(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var calls atomic.Int32
	s := spec("slow", func(context.Context, Input) (Result, error) {
		calls.Add(1)
		close(entered)
		<-release
		return Result{Summaries: []Summary{{Text: "late"}}}, nil
	})
	s.Timeout = 20 * time.Millisecond
	r := runner(t, []StageSpec{s}, &memoryStore{})
	got, err := r.Run(context.Background(), message(), State{})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if got.State.Traces[0].FailureCode != StageTimeout || got.State.Traces[0].Outcome != TimedOut {
		t.Fatal(got)
	}
	another := message()
	another.Identity.Message = "second"
	got, err = r.Run(context.Background(), another, State{})
	if err != nil || got.State.Traces[0].FailureCode != StageTimeout || calls.Load() != 1 {
		t.Fatalf("unbounded workers: %v %+v %d", err, got, calls.Load())
	}
	close(release)
	got, err = r.Run(context.Background(), message(), State{})
	if err != nil || len(got.State.Annotations) != 0 || calls.Load() != 1 {
		t.Fatalf("late replacement: %v %+v", err, got)
	}
}
func TestShutdownPreservesPending(t *testing.T) {
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	store := &memoryStore{}
	s := spec("cancel", func(ctx context.Context, _ Input) (Result, error) {
		close(entered)
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	r := runner(t, []StageSpec{s}, store)
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, message(), State{}); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, ErrPending) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.rows) != 0 {
		t.Fatal("shutdown stored failure")
	}
}
func TestIdentityConflictAndConfigRevision(t *testing.T) {
	store := &memoryStore{}
	calls := 0
	s := spec("summary", func(context.Context, Input) (Result, error) {
		calls++
		return Result{Summaries: []Summary{{Text: "saved"}}}, nil
	})
	r := runner(t, []StageSpec{s}, store)
	got, err := r.Run(context.Background(), message(), State{})
	if err != nil {
		t.Fatal(err)
	}
	got.State.Annotations[0].Summary.Text = "caller mutation"
	replay, err := r.Run(context.Background(), message(), State{})
	if err != nil || replay.State.Annotations[0].Summary.Text != "saved" {
		t.Fatalf("mutated: %v %+v", err, replay)
	}
	changed := message()
	changed.Original = "changed"
	if _, err = r.Run(context.Background(), changed, State{}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("conflicting provider call")
	}
	s.ConfigDigest = "config2"
	revised := runner(t, []StageSpec{s}, store)
	if _, err = revised.Run(context.Background(), changed, State{}); err != nil || calls != 2 {
		t.Fatalf("revision: %v %d", err, calls)
	}
	changed.Identity.Source = "endpoint/other-channel"
	if _, err = revised.Run(context.Background(), changed, State{}); err != nil || calls != 3 {
		t.Fatalf("collapsed channel: %v %d", err, calls)
	}
}
func TestStoreFailureBlocksSettlement(t *testing.T) {
	store := &memoryStore{err: errors.New("storage unavailable")}
	calls := 0
	r := runner(t, []StageSpec{spec("a", func(context.Context, Input) (Result, error) { calls++; return Result{}, nil })}, store)
	got, err := r.Run(context.Background(), message(), State{})
	if !errors.Is(err, ErrPending) || got.Disposition != Hold || calls != 0 {
		t.Fatalf("store fail-open %+v %v", got, err)
	}
}
func TestBoundsAndEncodedBudget(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 601), string([]byte{0xff})} {
		r := runner(t, []StageSpec{spec("a", func(context.Context, Input) (Result, error) { return Result{Summaries: []Summary{{Text: text}}}, nil })}, &memoryStore{})
		got, err := r.Run(context.Background(), message(), State{})
		if err != nil || got.State.Traces[0].FailureCode != InvalidOutput {
			t.Fatalf("invalid accepted: %v %+v", err, got)
		}
	}
	specs := make([]StageSpec, 4)
	for i, id := range []string{"a", "b", "c", "d"} {
		specs[i] = spec(id, func(context.Context, Input) (Result, error) {
			return Result{Summaries: []Summary{{Text: strings.Repeat("🔒", 600)}}}, nil
		})
	}
	got, err := runner(t, specs, &memoryStore{}).Run(context.Background(), message(), State{})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Traces[3].FailureCode != AnnotationLimit || got.State.Validate() != nil {
		t.Fatalf("JSON ceiling not enforced: %+v", got)
	}
}
func TestConcurrentReplayUsesOneOutcome(t *testing.T) {
	store := &memoryStore{}
	var calls atomic.Int32
	r := runner(t, []StageSpec{spec("a", func(context.Context, Input) (Result, error) { calls.Add(1); return Result{}, nil })}, store)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := r.Run(context.Background(), message(), State{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

type writeFailureStore struct{ memoryStore }

func (*writeFailureStore) PutIfAbsent(context.Context, Key, Record) (Record, error) {
	return Record{}, errors.New("write unavailable")
}
func TestPersistenceFailureAndUpstreamConflict(t *testing.T) {
	r := runner(t, []StageSpec{spec("a", func(context.Context, Input) (Result, error) { return Result{}, nil })}, &writeFailureStore{})
	if got, err := r.Run(context.Background(), message(), State{}); !errors.Is(err, ErrPending) || got.Disposition != Hold {
		t.Fatalf("lost write settled %+v %v", got, err)
	}
	store := &memoryStore{}
	first := spec("first", func(context.Context, Input) (Result, error) {
		return Result{Summaries: []Summary{{Text: "first"}}}, nil
	})
	var downstream atomic.Int32
	second := spec("second", func(context.Context, Input) (Result, error) { downstream.Add(1); return Result{}, nil })
	if _, err := runner(t, []StageSpec{first, second}, store).Run(context.Background(), message(), State{}); err != nil {
		t.Fatal(err)
	}
	first.Version = "2"
	first.Stage = StageFunc(func(context.Context, Input) (Result, error) {
		return Result{Summaries: []Summary{{Text: "changed upstream"}}}, nil
	})
	if _, err := runner(t, []StageSpec{first, second}, store).Run(context.Background(), message(), State{}); !errors.Is(err, ErrConflict) || downstream.Load() != 1 {
		t.Fatalf("stale downstream reused: %v", err)
	}
}
func TestFailedMarkerMustFitBeforePersistence(t *testing.T) {
	initial := State{Annotations: []Annotation{{SchemaVersion: 1, StageID: "initial", StageVersion: "1", Kind: "summary", Summary: Summary{Text: "old"}}}}
	store := &memoryStore{}
	s := spec("limit", func(context.Context, Input) (Result, error) {
		summaries := make([]Summary, 16)
		for i := range summaries {
			summaries[i].Text = "new"
		}
		return Result{Summaries: summaries}, nil
	})
	got, err := runner(t, []StageSpec{s}, store).Run(context.Background(), message(), initial)
	if err != nil || got.State.Traces[0].FailureCode != AnnotationLimit || len(got.State.Annotations) != 1 {
		t.Fatalf("bad limit result: %+v %v", got, err)
	}
}
