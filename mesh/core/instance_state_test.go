package mesh_test

import (
	"encoding/json"
	mesh "github.com/hollis-labs/substrate/mesh"
	"reflect"
	"testing"
	"time"
)

func TestStateProjectionKeepsWorkAndConnectivityIndependent(t *testing.T) {
	cases := []struct {
		name    string
		status  mesh.InstanceStatus
		detail  mesh.InstanceDetail
		session mesh.SessionState
		task    mesh.TaskState
		a2a     mesh.A2AState
	}{
		{"starting", mesh.InstanceStarting, mesh.InstanceDetail{}, mesh.SessionStarting, mesh.TaskSubmitted, mesh.A2ASubmitted},
		{"live", mesh.InstanceRunning, mesh.InstanceDetail{}, mesh.SessionRunning, mesh.TaskWorking, mesh.A2AWorking},
		{"detached-still-running", mesh.InstanceRunning, mesh.InstanceDetail{}, mesh.SessionDetached, mesh.TaskWorking, mesh.A2AWorking},
		{"paused-session", mesh.InstanceRunning, mesh.InstanceDetail{}, mesh.SessionPaused, mesh.TaskPaused, mesh.A2AWorking},
		{"orphaned-resumable", mesh.InstanceRunning, mesh.InstanceDetail{}, mesh.SessionOrphaned, mesh.TaskPaused, mesh.A2AWorking},
		{"input", mesh.InstanceWaiting, mesh.InstanceDetail{Waiting: mesh.WaitingInput}, mesh.SessionRunning, mesh.TaskInputRequired, mesh.A2AInputRequired},
		{"auth-while-detached", mesh.InstanceWaiting, mesh.InstanceDetail{Waiting: mesh.WaitingAuth}, mesh.SessionDetached, mesh.TaskAuthRequired, mesh.A2AAuthRequired},
		{"approval-while-paused", mesh.InstanceWaiting, mesh.InstanceDetail{Waiting: mesh.WaitingApproval}, mesh.SessionPaused, mesh.TaskInputRequired, mesh.A2AInputRequired},
		{"stopping", mesh.InstanceStopping, mesh.InstanceDetail{}, mesh.SessionRunning, mesh.TaskWorking, mesh.A2AWorking},
		{"completed", mesh.InstanceStopped, mesh.InstanceDetail{Stopped: mesh.StopCompleted}, mesh.SessionEnded, mesh.TaskCompleted, mesh.A2ACompleted},
		{"failed-after-orphan", mesh.InstanceStopped, mesh.InstanceDetail{Stopped: mesh.StopFailed}, mesh.SessionOrphaned, mesh.TaskFailed, mesh.A2AFailed},
		{"canceled-with-paused-session", mesh.InstanceStopped, mesh.InstanceDetail{Stopped: mesh.StopCanceled}, mesh.SessionPaused, mesh.TaskCanceled, mesh.A2ACanceled},
		{"rejected", mesh.InstanceRejected, mesh.InstanceDetail{}, mesh.SessionEnded, mesh.TaskRejected, mesh.A2ARejected},
		{"ended-not-a-result", mesh.InstanceRunning, mesh.InstanceDetail{}, mesh.SessionEnded, mesh.TaskWorking, mesh.A2AWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mesh.ProjectState(tc.status, tc.detail, tc.session)
			if err != nil {
				t.Fatal(err)
			}
			if got.Task != tc.task || got.A2A != tc.a2a || got.Instance != tc.status || got.Detail != tc.detail || got.Session != tc.session {
				t.Fatalf("lost orthogonal state or result: %+v", got)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip mesh.StateProjection
			if err := json.Unmarshal(b, &roundTrip); err != nil || roundTrip != got {
				t.Fatalf("adapter metadata lost on wire: %v", err)
			}
		})
	}
}
func TestProjectionRejectsAmbiguousDetailAndInstancePause(t *testing.T) {
	for _, tc := range []struct {
		status mesh.InstanceStatus
		detail mesh.InstanceDetail
	}{
		{mesh.InstanceStatus("paused"), mesh.InstanceDetail{}},
		{mesh.InstanceWaiting, mesh.InstanceDetail{}},
		{mesh.InstanceWaiting, mesh.InstanceDetail{Waiting: mesh.WaitingInput, Stopped: mesh.StopFailed}},
		{mesh.InstanceRunning, mesh.InstanceDetail{Waiting: mesh.WaitingInput}},
		{mesh.InstanceStopped, mesh.InstanceDetail{}},
		{mesh.InstanceStopped, mesh.InstanceDetail{Stopped: mesh.StopReason("unknown")}},
	} {
		if _, err := mesh.ProjectState(tc.status, tc.detail, mesh.SessionRunning); err == nil {
			t.Fatalf("accepted ambiguous status/detail %q %+v", tc.status, tc.detail)
		}
	}
	if _, err := mesh.ProjectState(mesh.InstanceRunning, mesh.InstanceDetail{}, mesh.SessionState("invalid")); err == nil {
		t.Fatal("accepted invalid session state")
	}
	rows := mesh.InstanceStateMappings()
	rows[0].Task = mesh.TaskFailed
	got, err := mesh.ProjectState(mesh.InstanceStarting, mesh.InstanceDetail{}, mesh.SessionStarting)
	if err != nil || got.Task != mesh.TaskSubmitted {
		t.Fatal("caller mutation changed projection table")
	}
	if !mesh.TaskPaused.Valid() {
		t.Fatal("existing per-task paused state was removed")
	}
}
func TestEnrollmentBindingAndPinnedSessionRoundTrip(t *testing.T) {
	definition := mesh.DefinitionRef{ID: "reviewer", Revision: "r2", Digest: "sha256:example"}
	enrollment := mesh.Agent{URN: "msg://agent/example/reviewer", Owner: "msg://user/example/owner", Definition: definition, Lifecycle: mesh.EnrollmentActive}
	now := time.Unix(1700000000, 0).UTC()
	result := mesh.ResolveResult{Agent: enrollment,
		Session:  &mesh.Session{URN: "msg://session/example/conversation", AgentURN: enrollment.URN, Definition: definition, ContextRef: "context:review", StoreRef: "store:conversation", State: mesh.SessionDetached, CreatedAt: now, UpdatedAt: now},
		Instance: &mesh.AgentInstance{ID: "execution-2", AgentURN: enrollment.URN, Definition: definition, SessionURN: "msg://session/example/conversation", NodeRef: "node:worker", RuntimeRef: "runtime:test", BindingFence: 42, LaunchRecordRef: "launch:execution-2", Status: mesh.InstanceWaiting, Detail: mesh.InstanceDetail{Waiting: mesh.WaitingApproval}, CreatedAt: now, UpdatedAt: now},
		Binding:  &mesh.BindingLease{AgentURN: enrollment.URN, SessionURN: "msg://session/example/conversation", InstanceID: "execution-2", Holder: "msg://service/example/host", ExpiresAt: now.Add(time.Minute), FencingToken: 42},
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got mesh.ResolveResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, got) {
		t.Fatal("wire records lost pin, binding, owner or execution references")
	}
	idle := mesh.ResolveResult{Agent: enrollment}
	b, err = json.Marshal(idle)
	if err != nil {
		t.Fatal(err)
	}
	var decoded mesh.ResolveResult
	if err = json.Unmarshal(b, &decoded); err != nil || decoded.Binding != nil || decoded.Instance != nil || decoded.Session != nil {
		t.Fatal("idle enrollment fabricated an active binding")
	}
}

func TestProjectionUsesA2AVersionOneWireEnum(t *testing.T) {
	got, err := mesh.ProjectState(mesh.InstanceWaiting, mesh.InstanceDetail{Waiting: mesh.WaitingInput}, mesh.SessionRunning)
	if err != nil {
		t.Fatal(err)
	}
	if got.A2A != "TASK_STATE_INPUT_REQUIRED" || got.Task != "input_required" {
		t.Fatal("wire enum and canonical task state were conflated")
	}
}
