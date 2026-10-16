package ledgertest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/ledger"
)

// Record builds a valid record for tests.
func Record(subject string, stage ledger.Stage, label string) ledger.Record {
	v := ledger.Version{Label: "v1", Hash: "h1"}
	return ledger.Record{
		IdempotencyKey: ledger.Key(subject, stage, v),
		Stage:          stage, SubjectRef: subject, Version: v,
		Input:       ledger.Input{Mode: ledger.ModeNone, Hash: ledger.HashInput([]byte(subject), nil)},
		Output:      ledger.Output{Label: label},
		Disposition: ledger.Suggested,
	}
}

// Run runs the suite against stores returned by newStore (one fresh store per case).
func Run(t *testing.T, newStore func() ledger.Store) {
	ctx := context.Background()
	t.Run("append is idempotent on the key", func(t *testing.T) {
		s := newStore()
		a, dup, err := s.Append(ctx, Record("s1", ledger.StageRule, "gh"))
		if err != nil || dup || a.DecisionID == "" || a.CreatedAt.IsZero() {
			t.Fatalf("%+v dup=%v err=%v", a, dup, err)
		}
		b, dup, err := s.Append(ctx, Record("s1", ledger.StageRule, "different-label-same-key"))
		if err != nil || !dup || b.DecisionID != a.DecisionID || b.Output.Label != "gh" {
			t.Fatalf("second append must return the first: %+v dup=%v err=%v", b, dup, err)
		}
		if got, _ := s.Query(ctx, ledger.Filter{}); len(got) != 1 {
			t.Fatalf("rows: %d", len(got))
		}
	})

	t.Run("concurrent duplicates write one row", func(t *testing.T) {
		s := newStore()
		var wg sync.WaitGroup
		var fresh int32
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, dup, err := s.Append(ctx, Record("s1", ledger.StageClassifier, "q")); err == nil && !dup {
					atomic.AddInt32(&fresh, 1)
				}
			}()
		}
		wg.Wait()
		if fresh != 1 {
			t.Fatalf("%d appends claimed to be first", fresh)
		}
	})

	t.Run("invalid records are rejected", func(t *testing.T) {
		s := newStore()
		for name, mut := range map[string]func(*ledger.Record){
			"no stage":      func(r *ledger.Record) { r.Stage = "" },
			"no subject":    func(r *ledger.Record) { r.SubjectRef = "" },
			"no label":      func(r *ledger.Record) { r.Output.Label = "" },
			"no version":    func(r *ledger.Record) { r.Version = ledger.Version{} },
			"no input hash": func(r *ledger.Record) { r.Input.Hash = "" },
			"bad confidence": func(r *ledger.Record) {
				c := 1.5
				r.Output.Confidence = &c
			},
			"pointer mode, no pointer": func(r *ledger.Record) { r.Input.Mode = ledger.ModePointer },
		} {
			r := Record("s-invalid", ledger.StageRule, "x")
			mut(&r)
			if _, _, err := s.Append(ctx, r); !errors.Is(err, ledger.ErrInvalid) {
				t.Errorf("%s: want ErrInvalid, got %v", name, err)
			}
		}
	})

	t.Run("disposition moves one way", func(t *testing.T) {
		s := newStore()
		r, _, _ := s.Append(ctx, Record("s1", ledger.StageRule, "gh"))
		mv := func(d ledger.Disposition) error {
			_, err := s.AppendEvent(ctx, ledger.Event{DecisionID: r.DecisionID, Kind: ledger.EventDisposition, Disposition: d})
			return err
		}
		if err := mv(ledger.Accepted); err != nil {
			t.Fatal(err)
		}
		if err := mv(ledger.Rejected); !errors.Is(err, ledger.ErrTransition) {
			t.Fatalf("accepted -> rejected: %v", err)
		}
		if err := mv(ledger.Superseded); err != nil {
			t.Fatal(err)
		}
		if err := mv(ledger.Applied); !errors.Is(err, ledger.ErrTransition) {
			t.Fatalf("superseded is terminal: %v", err)
		}
		if err := mv(ledger.Superseded); !errors.Is(err, ledger.ErrTransition) {
			t.Fatalf("a repeated transition is not an event: %v", err)
		}
		e, _ := s.Get(ctx, r.DecisionID)
		if e.Current() != ledger.Superseded || len(e.Events) != 2 || e.Events[0].Seq != 1 || e.Events[1].Seq != 2 {
			t.Fatalf("%+v", e)
		}
		if _, err := s.AppendEvent(ctx, ledger.Event{DecisionID: "nope", Kind: ledger.EventDisposition, Disposition: ledger.Accepted}); !errors.Is(err, ledger.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})

	t.Run("corrections are labels, originals untouched", func(t *testing.T) {
		s := newStore()
		a, _, _ := s.Append(ctx, Record("s1", ledger.StageClassifier, "question"))
		s.Append(ctx, Record("s2", ledger.StageClassifier, "statement"))
		if _, err := s.AppendEvent(ctx, ledger.Event{DecisionID: a.DecisionID, Kind: ledger.EventCorrection,
			Correction: &ledger.Correction{Label: "urgent", Actor: ""}}); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatalf("a correction without an actor must be refused: %v", err)
		}
		if _, err := s.AppendEvent(ctx, ledger.Event{DecisionID: a.DecisionID, Kind: ledger.EventCorrection,
			Correction: &ledger.Correction{Label: "urgent", Actor: "urn:hollis:user:c"}}); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(ctx, a.DecisionID)
		if l, c := e.Label(); l != "urgent" || !c || e.Record.Output.Label != "question" {
			t.Fatalf("label=%q corrected=%v original=%q", l, c, e.Record.Output.Label)
		}
		yes, no := true, false
		if got, _ := s.Query(ctx, ledger.Filter{Corrected: &yes}); len(got) != 1 || got[0].Record.SubjectRef != "s1" {
			t.Fatalf("corrected filter: %+v", got)
		}
		if got, _ := s.Query(ctx, ledger.Filter{Corrected: &no}); len(got) != 1 || got[0].Record.SubjectRef != "s2" {
			t.Fatalf("uncorrected filter: %+v", got)
		}
	})

	t.Run("outcomes link downstream", func(t *testing.T) {
		s := newStore()
		a, _, _ := s.Append(ctx, Record("s1", ledger.StageRule, "gh"))
		if _, err := s.AppendEvent(ctx, ledger.Event{DecisionID: a.DecisionID, Kind: ledger.EventOutcome, Outcome: &ledger.Outcome{Result: "maybe", Ref: "r"}}); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatalf("%v", err)
		}
		s.AppendEvent(ctx, ledger.Event{DecisionID: a.DecisionID, Kind: ledger.EventOutcome, Outcome: &ledger.Outcome{Result: "failure", Ref: "delivery/9"}})
		s.AppendEvent(ctx, ledger.Event{DecisionID: a.DecisionID, Kind: ledger.EventOutcome, Outcome: &ledger.Outcome{Result: "success", Ref: "delivery/10"}})
		e, _ := s.Get(ctx, a.DecisionID)
		if o, ok := e.Outcome(); !ok || o.Ref != "delivery/10" || len(e.Events) != 2 {
			t.Fatalf("%+v", e)
		}
	})

	t.Run("query filters and limits", func(t *testing.T) {
		s := newStore()
		s.Append(ctx, Record("s1", ledger.StageRule, "a"))
		s.Append(ctx, Record("s1", ledger.StageClassifier, "b"))
		s.Append(ctx, Record("s2", ledger.StageRule, "c"))
		if got, _ := s.Query(ctx, ledger.Filter{Stage: ledger.StageRule}); len(got) != 2 {
			t.Fatalf("stage: %d", len(got))
		}
		if got, _ := s.Query(ctx, ledger.Filter{SubjectRef: "s1"}); len(got) != 2 {
			t.Fatalf("subject: %d", len(got))
		}
		if got, _ := s.Query(ctx, ledger.Filter{Limit: 1}); len(got) != 1 {
			t.Fatalf("limit: %d", len(got))
		}
		if got, _ := s.Query(ctx, ledger.Filter{Since: time.Now().Add(time.Hour)}); len(got) != 0 {
			t.Fatalf("since: %d", len(got))
		}
	})

	t.Run("returned values are copies", func(t *testing.T) {
		s := newStore()
		r := Record("s1", ledger.StageRule, "a")
		r.Alternatives = []ledger.Alternative{{Label: "x", Why: "orig"}}
		r.Output.Probabilities = map[string]float64{"a": 1}
		a, _, _ := s.Append(ctx, r)
		a.Alternatives[0].Why = "mutated"
		a.Output.Probabilities["a"] = 0
		e, _ := s.Get(ctx, a.DecisionID)
		if e.Record.Alternatives[0].Why != "orig" || e.Record.Output.Probabilities["a"] != 1 {
			t.Fatalf("store state aliased: %+v", e.Record)
		}
	})
}
