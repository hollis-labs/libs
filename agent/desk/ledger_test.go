package desk

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLedgerLifecycle(t *testing.T) {
	c := newClock()
	l := NewLedger(c.now)
	topic, err := l.Open("", "Orchestration fixes", "focus", Ref{Kind: "thread", ID: "th-1"})
	if err != nil || topic.ID == "" || topic.Status != TopicOpen || len(topic.Refs) != 1 {
		t.Fatalf("open: %+v, %v", topic, err)
	}
	if _, err := l.Open(topic.ID, "again", ""); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate open: %v", err)
	}
	c.advance(time.Minute)
	if err := l.Attach(topic.ID, Work{ID: "w1", Kind: KindResearch, Summary: "find the stall", State: WorkRunning, Ref: Ref{Kind: "job", ID: "job-7"}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Attach(topic.ID, Work{ID: "w2", Kind: KindDelegate, Summary: "fix tether"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Attach(topic.ID, Work{ID: "w2"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate work: %v", err)
	}
	found, ok := l.Find(Ref{Kind: "job", ID: "job-7"})
	if !ok || found.ID != topic.ID {
		t.Fatalf("find by job: %v %+v", ok, found)
	}
	got, _ := l.Get(topic.ID)
	if n := len(got.InFlight()); n != 2 {
		t.Fatalf("in flight %d", n)
	}
	if got.Work[0].Started.IsZero() || !got.Work[1].Started.IsZero() {
		t.Fatalf("started stamps: %+v", got.Work)
	}
	c.advance(time.Minute)
	if err := l.Progress(topic.ID, "w1", WorkDone, "the stall is in compaction"); err != nil {
		t.Fatal(err)
	}
	if err := l.Progress(topic.ID, "w1", WorkRunning, ""); err == nil {
		t.Fatal("a finished work moved again")
	}
	if err := l.Progress(topic.ID, "nope", WorkDone, ""); !errors.Is(err, ErrNoWork) {
		t.Fatalf("unknown work: %v", err)
	}
	q, err := l.Ask(topic.ID, "", "Restart tether now?")
	if err != nil || q.ID == "" || !l.HasOpenQuestion(topic.ID) {
		t.Fatalf("ask: %+v %v", q, err)
	}
	if err := l.Answer(topic.ID, q.ID, "yes"); err != nil || l.HasOpenQuestion(topic.ID) {
		t.Fatalf("answer: %v", err)
	}
	if err := l.Answer(topic.ID, q.ID, "again"); err == nil {
		t.Fatal("answered twice")
	}
	got, _ = l.Get(topic.ID)
	if got.Work[0].Result != "the stall is in compaction" || got.Work[0].Finished.IsZero() || len(got.InFlight()) != 1 {
		t.Fatalf("after progress: %+v", got.Work)
	}
	if !got.Updated.After(got.Created) {
		t.Fatal("updated not stamped")
	}
	if err := l.Close(topic.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.Attach(topic.ID, Work{ID: "w3"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("attach to closed: %v", err)
	}
	if _, err := l.Ask(topic.ID, "", "?"); !errors.Is(err, ErrClosed) {
		t.Fatalf("ask on closed: %v", err)
	}
	if err := l.Progress(topic.ID, "w2", WorkCanceled, ""); err != nil {
		t.Fatalf("finishing work on a closed topic: %v", err)
	}
	if len(l.OpenTopics()) != 0 {
		t.Fatal("closed topic listed as open")
	}
	if err := l.Reopen(topic.ID); err != nil || len(l.OpenTopics()) != 1 {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := l.Get("missing"); !errors.Is(err, ErrNoTopic) {
		t.Fatalf("missing: %v", err)
	}
}

func TestLedgerCopiesDoNotAlias(t *testing.T) {
	l := NewLedger(nil)
	topic, _ := l.Open("a", "A", "")
	_ = l.Attach("a", Work{ID: "w"})
	got, _ := l.Get(topic.ID)
	got.Work[0].Summary = "changed outside"
	again, _ := l.Get(topic.ID)
	if again.Work[0].Summary != "" {
		t.Fatal("a returned topic aliases the ledger")
	}
}

func TestLedgerLinkMovesARef(t *testing.T) {
	l := NewLedger(nil)
	_, _ = l.Open("a", "A", "", Ref{Kind: "job", ID: "j"})
	_, _ = l.Open("b", "B", "")
	if err := l.Link("b", Ref{Kind: "job", ID: "j"}); err != nil {
		t.Fatal(err)
	}
	a, _ := l.Get("a")
	if len(a.Refs) != 0 {
		t.Fatalf("ref still on a: %v", a.Refs)
	}
	if found, _ := l.Find(Ref{Kind: "job", ID: "j"}); found.ID != "b" {
		t.Fatalf("ref on %s", found.ID)
	}
}

func TestLedgerListOrder(t *testing.T) {
	c := newClock()
	l := NewLedger(c.now)
	for _, id := range []TopicID{"a", "b", "c"} {
		_, _ = l.Open(id, string(id), "")
		c.advance(time.Second)
	}
	_ = l.Attach("a", Work{ID: "w"})
	list := l.List(nil)
	if list[0].ID != "a" || list[1].ID != "c" || list[2].ID != "b" {
		t.Fatalf("order %s %s %s", list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestLedgerJSONRoundTrip(t *testing.T) {
	c := newClock()
	l := NewLedger(c.now)
	_, _ = l.Open("", "One", "", Ref{Kind: "thread", ID: "x"})
	_ = l.Attach("t1", Work{ID: "w", Kind: KindResearch, State: WorkRunning, Ref: Ref{Kind: "job", ID: "j"}})
	_, _ = l.Ask("t1", "", "ok?")
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var back Ledger
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got, err := back.Get("t1")
	if err != nil || got.Title != "One" || len(got.Work) != 1 || !back.HasOpenQuestion("t1") {
		t.Fatalf("restored %+v, %v", got, err)
	}
	if found, ok := back.Find(Ref{Kind: "job", ID: "j"}); !ok || found.ID != "t1" {
		t.Fatal("restored ledger lost the ref index")
	}
	next, _ := back.Open("", "Two", "")
	if next.ID == "t1" {
		t.Fatal("restored ledger reused a generated id")
	}
	if err := json.Unmarshal([]byte(`{"topics":[{"id":"a"},{"id":"a"}]}`), &back); err == nil {
		t.Fatal("repeated ids accepted")
	}
}

func TestLedgerConcurrent(t *testing.T) {
	l := NewLedger(nil)
	_, _ = l.Open("a", "A", "")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprint(i)
			_ = l.Attach("a", Work{ID: id, State: WorkRunning, Ref: Ref{Kind: "job", ID: id}})
			_ = l.Progress("a", id, WorkDone, "r")
			_, _ = l.Ask("a", "q"+id, "?")
			_ = l.OpenTopics()
			_, _ = json.Marshal(l)
		}()
	}
	wg.Wait()
	got, _ := l.Get("a")
	if len(got.Work) != 20 || len(got.Questions) != 20 || len(got.InFlight()) != 0 {
		t.Fatalf("work %d questions %d", len(got.Work), len(got.Questions))
	}
}
