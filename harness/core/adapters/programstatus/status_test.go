package programstatus

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

func osc(body string) []byte { return []byte("\x1b]7501;" + body + "\x1b\\") }

func TestParseSpecReport(t *testing.T) {
	msg := base64.StdEncoding.EncodeToString([]byte("Approve deployment?"))
	r, err := Parse(osc("state=working:badpair:state=blocked:kind=permission:id=eu-west:progress=40:msg=" + msg))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != Blocked || r.Kind != "permission" || r.ID != "eu-west" || r.Message != "Approve deployment?" || r.Progress == nil || *r.Progress != 40 {
		t.Fatalf("unexpected report: %+v", r)
	}
}

func TestParseRejectsUnsafeOrOversizedReports(t *testing.T) {
	for name, seq := range map[string][]byte{
		"unknown state": osc("state=paused"),
		"invalid id":    osc("state=working:id=foo//bar"),
		"bad id byte":   osc("state=working:id=foo%bar"),
		"empty id":      osc("state=working:id="),
		"control text":  osc("state=blocked:msg=" + base64.StdEncoding.EncodeToString([]byte("line\nnext"))),
		"bad base64":    osc("state=done:msg=A"),
		"too long":      osc("state=working:msg=" + strings.Repeat("A", 4100)),
		"no terminator": []byte("\x1b]7501;state=done"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(seq); err == nil {
				t.Fatal("expected invalid report")
			}
		})
	}
}

func TestParseToleratesUnknownAndInvalidOptionalValues(t *testing.T) {
	r, err := Parse(append([]byte("\x1b]7501;state=working:future=ok:progress=300:app=bad/app"), '\a'))
	if err != nil {
		t.Fatal(err)
	}
	if r.Progress != nil || r.App != "" || r.State != Working {
		t.Fatalf("optional value handling: %+v", r)
	}
}

func TestTrackerFencesGenerationAndSeparatesStatus(t *testing.T) {
	var tracker Tracker
	if !tracker.Start(1) || tracker.Start(1) {
		t.Fatal("generation start guard failed")
	}
	r, _ := Parse(osc("state=blocked:kind=question:id=child:msg=V2hhdCBuZXh0Pw=="))
	s, ok := tracker.Apply(1, 7, r)
	if !ok || !s.Alive || s.Work != Blocked || s.Attention != "question" || s.Message != "What next?" {
		t.Fatalf("bad blocked projection: %+v accepted=%v", s, ok)
	}
	if _, ok := tracker.Apply(1, 7, Report{State: Idle}); ok {
		t.Fatal("duplicate source sequence accepted")
	}
	if !tracker.Start(2) {
		t.Fatal("new generation refused")
	}
	if _, ok := tracker.Apply(1, 8, Report{State: Blocked}); ok {
		t.Fatal("late prior-process report accepted")
	}
	s, ok = tracker.Apply(2, 1, Report{State: Done})
	if !ok || s.Work != Done || s.Attention != "done" {
		t.Fatalf("bad done projection: %+v", s)
	}
	s, ok = tracker.Exit(2)
	if !ok || s.Alive || s.Work != Done {
		t.Fatalf("done should survive process exit: %+v", s)
	}
}

func TestTrackerClearSubtree(t *testing.T) {
	var tracker Tracker
	tracker.Start(1)
	tracker.Apply(1, 1, Report{ID: "build", State: Working})
	tracker.Apply(1, 2, Report{ID: "build/test", State: Blocked, Kind: "permission"})
	s, ok := tracker.Apply(1, 3, Report{ID: "build", State: Clear})
	if !ok || s.Work != "" || s.Attention != "" {
		t.Fatalf("clear did not remove subtree: %+v", s)
	}
}

func TestScannerFindsSplitReportsAndKeepsPTYOffsetDistinct(t *testing.T) {
	var scanner Scanner
	first := append([]byte("prompt> "), osc("state=working:id=job")...)
	first = append(first, []byte(" ordinary output ")...)
	cut := len(first) - 4
	if got := scanner.Feed(100, first[:cut]); len(got) != 1 || got[0].RawOffset != 108 || got[0].Report.State != Working {
		t.Fatalf("first observation: %+v", got)
	}
	second := append(first[cut:], osc("state=done:id=job")...)
	got := scanner.Feed(100+int64(cut), second)
	if len(got) != 1 || got[0].Report.State != Done || got[0].RawOffset != 100+int64(len(first)) {
		t.Fatalf("second observation: %+v", got)
	}
}

func TestScannerDiscardsPartialOnGap(t *testing.T) {
	var scanner Scanner
	scanner.Feed(0, []byte("\x1b]7501;state=blocked"))
	if got := scanner.Feed(100, []byte("\x1b\\")); len(got) != 0 {
		t.Fatalf("gap completed stale partial report: %+v", got)
	}
}

func TestEventUsesDurableCursorOnlyAfterAppend(t *testing.T) {
	s := Snapshot{ProcessGeneration: 4, SourceSeq: 12, Alive: true, Work: Blocked, Attention: "permission", RecordID: "child", Message: "Approve?"}
	base := runtimeevents.Event{SchemaVersion: runtimeevents.SchemaVersion, ID: "status-1", Time: time.Now(), SessionID: "session-1", Sequence: 9, Cursor: "old-cursor"}
	e, err := s.Event(base, 123)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != runtimeevents.KindSessionStatusChanged || e.RawOffset == nil || *e.RawOffset != 123 || e.Cursor != "" || e.Class.PayloadSchema != payloadSchema {
		t.Fatalf("event projection: %+v", e)
	}
	var body map[string]any
	if err := json.Unmarshal(e.Payload, &body); err != nil || body["liveness"] != "alive" || body["attention"] != "permission" || body["process_generation"] != float64(4) {
		t.Fatalf("event payload: %v %+v", err, body)
	}
}
