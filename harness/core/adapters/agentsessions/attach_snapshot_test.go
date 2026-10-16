package agentsessions

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAttachSnapshotRetention(t *testing.T) {
	for _, tt := range []struct {
		name   string
		writes []string
		since  int64
		closed bool
		want   AttachSnapshot
		replay string
	}{
		{name: "empty", want: AttachSnapshot{}},
		{name: "beginning", writes: []string{"ab"}, want: AttachSnapshot{0, 2}, replay: "ab"},
		{name: "partial eviction", writes: []string{"abc", "de", "f"}, want: AttachSnapshot{2, 6}, replay: "cdef"},
		{name: "oversized chunk", writes: []string{"abcdefghij"}, want: AttachSnapshot{6, 10}, replay: "ghij"},
		{name: "evicted resume", writes: []string{"abcdefghij"}, since: 2, want: AttachSnapshot{6, 10}, replay: "ghij"},
		{name: "within retained window", writes: []string{"abcdefghij"}, since: 8, want: AttachSnapshot{6, 10}, replay: "ij"},
		{name: "at head", writes: []string{"abcdef"}, since: 6, want: AttachSnapshot{2, 6}},
		{name: "ahead of head", writes: []string{"abcdef"}, since: 99, want: AttachSnapshot{2, 6}},
		{name: "closed replay", writes: []string{"abcdef"}, closed: true, want: AttachSnapshot{2, 6}, replay: "cdef"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := newAttachBroker(4, 4)
			defer b.close()
			for _, chunk := range tt.writes {
				if _, err := b.Write([]byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			if tt.closed {
				b.close()
			}
			replay, ch, cancel, snapshot := b.subscribeSnapshot(0, tt.since)
			defer cancel()
			if snapshot != tt.want || string(replay) != tt.replay {
				t.Fatalf("snapshot=%+v replay=%q, want %+v %q", snapshot, replay, tt.want, tt.replay)
			}
			if tt.closed {
				if _, ok := <-ch; ok {
					t.Fatal("closed broker returned open subscription")
				}
			}
		})
	}
}

// Writes and subscriptions race deliberately; each snapshot must describe its
// own replay, even if the producer immediately evicts it from the broker.
func TestAttachSnapshotConcurrentWrites(t *testing.T) {
	b := newAttachBroker(7, 4)
	defer b.close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _ = b.Write([]byte("xyz"))
		}
	}()
	for i := 0; i < 100; i++ {
		replay, _, cancel, snapshot := b.subscribeSnapshot(0, 0)
		cancel()
		if snapshot.NextOffset-snapshot.OldestOffset != int64(len(replay)) || snapshot.OldestOffset < 0 || len(replay) > 7 {
			t.Fatalf("inconsistent replay snapshot: %+v length=%d", snapshot, len(replay))
		}
	}
	wg.Wait()
	replay, _, cancel, snapshot := b.subscribeSnapshot(0, 0)
	defer cancel()
	if snapshot != (AttachSnapshot{293, 300}) || string(replay) != "zxyzxyz" {
		t.Fatalf("final snapshot=%+v replay=%q", snapshot, replay)
	}
}

type snapshotAttachmentSink struct{ created, detached int }

func (s *snapshotAttachmentSink) CreateClientAttachment(string, string, string, time.Time) error {
	s.created++
	return nil
}
func (s *snapshotAttachmentSink) DetachClientAttachment(string, time.Time) error {
	s.detached++
	return nil
}

func snapshotManager(b *attachBroker) (*Manager, *snapshotAttachmentSink) {
	sink := &snapshotAttachmentSink{}
	m := NewManager(nil).WithAttachmentSink(sink)
	m.registry["s"] = &entry{broker: b}
	return m, sink
}

func assertSnapshotReleased(t *testing.T, m *Manager, b *attachBroker, sink *snapshotAttachmentSink) {
	t.Helper()
	m.mu.RLock()
	attached := m.registry["s"].attachCount
	m.mu.RUnlock()
	b.mu.Lock()
	subscribers := len(b.subs)
	b.mu.Unlock()
	if attached != 0 || subscribers != 0 || sink.created != 1 || sink.detached != 1 {
		t.Fatalf("leaked attachment: refs=%d subscribers=%d persisted=%d/%d", attached, subscribers, sink.created, sink.detached)
	}
}

func TestAttachSnapshotCallbackOrdering(t *testing.T) {
	b := newAttachBroker(4, 4)
	_, _ = b.Write([]byte("abcdef"))
	m, sink := snapshotManager(b)
	var output bytes.Buffer
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- m.AttachWith(context.Background(), "s", &output, AttachOptions{OnSnapshot: func(snapshot AttachSnapshot) error {
			calls++
			if output.Len() != 0 || snapshot != (AttachSnapshot{2, 6}) {
				return errors.New("callback did not precede captured replay")
			}
			// Both locks must be released, and the live subscriber must already
			// exist so this write follows the captured replay without loss.
			m.mu.Lock()
			m.mu.Unlock()
			if _, err := b.Write([]byte("GH")); err != nil {
				return err
			}
			b.close()
			return nil
		}})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback blocked on an attach lock")
	}
	if calls != 1 || output.String() != "cdefGH" {
		t.Fatalf("calls=%d output=%q", calls, output.String())
	}
	assertSnapshotReleased(t, m, b, sink)
}

func TestAttachSnapshotCallbackFailure(t *testing.T) {
	b := newAttachBroker(4, 4)
	defer b.close()
	_, _ = b.Write([]byte("abcdef"))
	m, sink := snapshotManager(b)
	var output bytes.Buffer
	refused := errors.New("consumer refused snapshot")
	err := m.AttachWith(context.Background(), "s", &output, AttachOptions{OnSnapshot: func(AttachSnapshot) error { return refused }})
	if !errors.Is(err, refused) || output.Len() != 0 {
		t.Fatalf("err=%v bytes=%d", err, output.Len())
	}
	assertSnapshotReleased(t, m, b, sink)
}

func TestAttachSnapshotUnavailable(t *testing.T) {
	m := NewManager(nil)
	m.registry["disabled"] = &entry{}
	for _, tt := range []struct {
		id   string
		want error
	}{{"missing", ErrSessionNotRunning}, {"disabled", ErrAttachDisabled}} {
		t.Run(tt.id, func(t *testing.T) {
			called := false
			var output bytes.Buffer
			err := m.AttachWith(context.Background(), tt.id, &output, AttachOptions{OnSnapshot: func(AttachSnapshot) error { called = true; return nil }})
			if !errors.Is(err, tt.want) || called || output.Len() != 0 {
				t.Fatalf("err=%v callback=%v bytes=%d", err, called, output.Len())
			}
		})
	}
}

func TestAttachSnapshotOptionalCallback(t *testing.T) {
	b := newAttachBroker(4, 4)
	_, _ = b.Write([]byte("abcdef"))
	b.close()
	m, sink := snapshotManager(b)
	var output bytes.Buffer
	if err := m.AttachWith(context.Background(), "s", &output, AttachOptions{SinceSeq: 3}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "def" {
		t.Fatalf("unchanged resume replay=%q", output.String())
	}
	assertSnapshotReleased(t, m, b, sink)
}
