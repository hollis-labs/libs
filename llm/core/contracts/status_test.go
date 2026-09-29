package agentcontracts

import (
	"encoding/json"
	"testing"
)

var (
	allStatuses = []InstanceStatus{StatusStarting, StatusRunning, StatusWaiting, StatusPaused, StatusStopping, StatusStopped, StatusRejected}
	allWaiting  = []WaitingReason{WaitingInput, WaitingAuth, WaitingApproval}
	allStopped  = []StoppedReason{ReasonCompleted, ReasonFailed, ReasonCanceled}
)

func TestToA2A(t *testing.T) {
	// Statuses that ignore both the waiting reason and the stopped detail.
	plain := map[InstanceStatus]A2ATaskState{
		StatusStarting: A2ASubmitted,
		StatusRunning:  A2AWorking,
		StatusPaused:   A2AWorking, // lossy: A2A has no paused
		StatusStopping: A2AWorking, // lossy: A2A has no stopping
		StatusRejected: A2ARejected,
	}
	for _, w := range append([]WaitingReason{""}, allWaiting...) {
		for _, r := range append([]StoppedReason{""}, allStopped...) {
			for s, want := range plain {
				if got := s.ToA2A(w, StoppedDetail{Reason: r}); got != want {
					t.Errorf("%s (waiting=%q stopped=%q) = %q, want %q", s, w, r, got, want)
				}
			}
		}
	}

	waiting := map[WaitingReason]A2ATaskState{
		WaitingInput: A2AInputRequired,
		WaitingAuth:  A2AAuthRequired,
		// PROVISIONAL: no A2A state cleanly means "waiting on human approval".
		WaitingApproval: A2AInputRequired,
		"":              "",
		"bogus":         "",
	}
	for w, want := range waiting {
		if got := StatusWaiting.ToA2A(w, StoppedDetail{}); got != want {
			t.Errorf("waiting.%q = %q, want %q", w, got, want)
		}
	}

	stopped := map[StoppedReason]A2ATaskState{
		ReasonCompleted: A2ACompleted,
		ReasonFailed:    A2AFailed,
		ReasonCanceled:  A2ACanceled,
		"":              "",
		"done":          "", // the legacy word is not an alias
	}
	for r, want := range stopped {
		if got := StatusStopped.ToA2A("", StoppedDetail{Reason: r}); got != want {
			t.Errorf("stopped.%q = %q, want %q", r, got, want)
		}
	}

	if got := InstanceStatus("orphaned").ToA2A("", StoppedDetail{}); got != "" {
		t.Errorf("orphaned is not a status; got %q", got)
	}
}

func TestToA2AResultIsValidOrEmpty(t *testing.T) {
	for _, s := range allStatuses {
		for _, w := range allWaiting {
			for _, r := range allStopped {
				if got := s.ToA2A(w, StoppedDetail{Reason: r}); !got.Valid() {
					t.Errorf("%s/%s/%s produced invalid A2A state %q", s, w, r, got)
				}
			}
		}
	}
}

func TestStoppedDetail(t *testing.T) {
	valid := []StoppedDetail{
		{Reason: ReasonCompleted},
		{Reason: ReasonFailed},
		{Reason: ReasonFailed, Cause: CauseError},
		{Reason: ReasonFailed, Cause: CauseCrashed},
		{Reason: ReasonFailed, Cause: CauseLost},
		{Reason: ReasonCanceled},
		{Reason: ReasonCanceled, Cause: CauseRequested},
		{Reason: ReasonCanceled, Cause: CauseKilled},
	}
	for _, d := range valid {
		if !d.Valid() {
			t.Errorf("%+v should be valid: %v", d, d.Validate())
		}
	}
	invalid := []StoppedDetail{
		{},
		{Reason: "done"},
		{Reason: ReasonCompleted, Cause: CauseError}, // completed has no cause
		{Reason: ReasonCompleted, Cause: CauseRequested},
		{Reason: ReasonFailed, Cause: CauseKilled},
		{Reason: ReasonFailed, Cause: CauseRequested},
		{Reason: ReasonCanceled, Cause: CauseLost},
		{Reason: ReasonCanceled, Cause: CauseCrashed},
		{Reason: ReasonCanceled, Cause: CauseError},
		{Reason: ReasonFailed, Cause: "orphaned"},
	}
	for _, d := range invalid {
		if d.Valid() {
			t.Errorf("%+v should be invalid", d)
		}
	}
}

func TestEnumsRejectOutsiders(t *testing.T) {
	if InstanceStatus("orphaned").Valid() || InstanceStatus("").Valid() || InstanceStatus("done").Valid() {
		t.Error("InstanceStatus accepted a value outside the fixed seven")
	}
	if StoppedReason("done").Valid() {
		t.Error(`"done" was hard-renamed to "completed"; there is no alias`)
	}
	if WaitingReason("").Valid() || A2ATaskState("").Valid() || StoppedCause("").Valid() {
		t.Error("empty values must not be Valid")
	}
	if len(allStatuses) != 7 {
		t.Error("test list out of step with the seven statuses")
	}
}

// Every value an enum calls Valid must survive JSON unchanged.
func TestValidValuesRoundTripThroughJSON(t *testing.T) {
	type wrap struct {
		S InstanceStatus `json:"s"`
		W WaitingReason  `json:"w"`
		R StoppedReason  `json:"r"`
		C StoppedCause   `json:"c"`
		A A2ATaskState   `json:"a"`
		L Lifetime       `json:"l"`
		P ResumePolicy   `json:"p"`
		I Isolation      `json:"i"`
		T Trust          `json:"t"`
	}
	for _, s := range allStatuses {
		for _, w := range allWaiting {
			for _, r := range allStopped {
				in := wrap{s, w, r, CauseError, A2ACompleted, LifetimeOneShot, ResumeAlways, IsolationNone, TrustTrusted}
				raw, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				var out wrap
				if err := json.Unmarshal(raw, &out); err != nil {
					t.Fatal(err)
				}
				if out != in {
					t.Errorf("round trip changed %+v into %+v", in, out)
				}
			}
		}
	}
}
