package loopdetect

import (
	"encoding/json"
	"testing"
)

// The options below are deliberately unvalidated; these tests pin what
// happens today (see the "Degenerate options" section of doc.go).

func TestWithThreshold_Zero_FiresOnFirstRecord(t *testing.T) {
	d := New(WithThreshold(0))
	det, ok := d.Record(Signal{SessionID: "s", TurnID: "t1", ToolName: "x", Args: json.RawMessage(`{}`)})
	if !ok {
		t.Fatal("expected detection on first Record with threshold 0")
	}
	if det.Count != 1 || det.DetectedAtTurn != "t1" {
		t.Fatalf("unexpected detection: %+v", det)
	}
}

func TestWithWindowSize_Zero_NeverFires(t *testing.T) {
	d := New(WithWindowSize(0))
	for i := 0; i < 20; i++ {
		if _, ok := d.Record(Signal{SessionID: "s", ToolName: "x", Args: json.RawMessage(`{}`)}); ok {
			t.Fatalf("record %d: detection fired with window size 0", i)
		}
	}
}

func TestWithWindowSize_Negative_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for negative window size")
		}
	}()
	d := New(WithWindowSize(-1))
	d.Record(Signal{SessionID: "s", ToolName: "x"})
}

func TestWithThreshold_Negative_FiresOnFirstRecord(t *testing.T) {
	d := New(WithThreshold(-5))
	if _, ok := d.Record(Signal{SessionID: "s", ToolName: "x"}); !ok {
		t.Fatal("expected detection with negative threshold")
	}
}

func TestWithMaxSessions_NonPositive_KeepsDefault(t *testing.T) {
	for _, n := range []int{0, -1} {
		if d := New(WithMaxSessions(n)); d.maxSessions != DefaultMaxSessions {
			t.Fatalf("WithMaxSessions(%d): maxSessions = %d, want default %d", n, d.maxSessions, DefaultMaxSessions)
		}
	}
}

// While a fired fingerprint is still in the window, Record reports no
// detection for any other fingerprint either (suppression is per session,
// not per fingerprint). Pinned as documented in the README's known
// limitations.
func TestSuppression_BlocksOtherFingerprintsWhileFiredOneInWindow(t *testing.T) {
	d := New()
	rec := func(tool string) bool {
		_, ok := d.Record(Signal{SessionID: "s", ToolName: tool})
		return ok
	}
	rec("a")
	rec("a")
	if !rec("a") {
		t.Fatal("expected detection for a")
	}
	rec("b")
	rec("b")
	if rec("b") {
		t.Fatal("b reached threshold while a was still in the window; expected suppression")
	}
}
