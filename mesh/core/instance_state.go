package mesh

import "fmt"

// InstanceStatus is work state, orthogonal to SessionState connectivity. It
// contains no paused state; paused sessions may retain a running/waiting instance.
type InstanceStatus string

const (
	InstanceStarting InstanceStatus = "starting"
	InstanceRunning  InstanceStatus = "running"
	InstanceWaiting  InstanceStatus = "waiting"
	InstanceStopping InstanceStatus = "stopping"
	InstanceStopped  InstanceStatus = "stopped"
	InstanceRejected InstanceStatus = "rejected"
)

type WaitingReason string

const (
	WaitingInput    WaitingReason = "input"
	WaitingAuth     WaitingReason = "auth"
	WaitingApproval WaitingReason = "approval"
)

type StopReason string

const (
	StopCompleted StopReason = "completed"
	StopFailed    StopReason = "failed"
	StopCanceled  StopReason = "canceled"
)

// InstanceDetail distinguishes interruption and terminal outcomes. Only the
// reason corresponding to Status may be set; running is not a task outcome.
type InstanceDetail struct {
	Waiting WaitingReason `json:"waiting,omitempty"`
	Stopped StopReason    `json:"stopped,omitempty"`
}

// A2AState is the A2A v1.0 wire task vocabulary. paused has no A2A enum value.
// Wire values are distinct from the mesh canonical TaskState spellings.
type A2AState string

const (
	A2ASubmitted     A2AState = "TASK_STATE_SUBMITTED"
	A2AWorking       A2AState = "TASK_STATE_WORKING"
	A2AInputRequired A2AState = "TASK_STATE_INPUT_REQUIRED"
	A2AAuthRequired  A2AState = "TASK_STATE_AUTH_REQUIRED"
	A2ACompleted     A2AState = "TASK_STATE_COMPLETED"
	A2AFailed        A2AState = "TASK_STATE_FAILED"
	A2ACanceled      A2AState = "TASK_STATE_CANCELED"
	A2ARejected      A2AState = "TASK_STATE_REJECTED"
)

// InstanceStateMapping is a row in the explicit work-state projection table.
// SessionState is deliberately separate: connectivity never invents a result.
type InstanceStateMapping struct {
	Status InstanceStatus
	Detail InstanceDetail
	Task   TaskState
	A2A    A2AState
}

// InstanceStateMappings returns an independent table, with these rows:
//
//	InstanceStatus/detail  TaskState       A2A v1.0 wire state
//	starting               submitted       TASK_STATE_SUBMITTED
//	running                working         TASK_STATE_WORKING
//	waiting/input          input_required  TASK_STATE_INPUT_REQUIRED
//	waiting/auth           auth_required   TASK_STATE_AUTH_REQUIRED
//	waiting/approval       input_required  TASK_STATE_INPUT_REQUIRED
//	stopping               working         TASK_STATE_WORKING
//	stopped/completed      completed       TASK_STATE_COMPLETED
//	stopped/failed         failed          TASK_STATE_FAILED
//	stopped/canceled       canceled        TASK_STATE_CANCELED
//	rejected               rejected        TASK_STATE_REJECTED
//
// Waiting reasons and instance status are retained in the projection metadata.
// Session states starting, running, detached and ended retain the mapped work
// state. Paused and orphaned sessions map active submitted/working work to
// TaskPaused and A2AWorking, while preserving waiting and terminal outcomes.
//
// Ended alone never implies completed. Detached is alive without a connected
// control plane; orphaned is a gone process with resumable state. A2A adapters
// carry session/instance state and waiting reason as extension metadata.
func InstanceStateMappings() []InstanceStateMapping {
	return []InstanceStateMapping{
		{InstanceStarting, InstanceDetail{}, TaskSubmitted, A2ASubmitted},
		{InstanceRunning, InstanceDetail{}, TaskWorking, A2AWorking},
		{InstanceWaiting, InstanceDetail{Waiting: WaitingInput}, TaskInputRequired, A2AInputRequired},
		{InstanceWaiting, InstanceDetail{Waiting: WaitingAuth}, TaskAuthRequired, A2AAuthRequired},
		{InstanceWaiting, InstanceDetail{Waiting: WaitingApproval}, TaskInputRequired, A2AInputRequired},
		{InstanceStopping, InstanceDetail{}, TaskWorking, A2AWorking},
		{InstanceStopped, InstanceDetail{Stopped: StopCompleted}, TaskCompleted, A2ACompleted},
		{InstanceStopped, InstanceDetail{Stopped: StopFailed}, TaskFailed, A2AFailed},
		{InstanceStopped, InstanceDetail{Stopped: StopCanceled}, TaskCanceled, A2ACanceled},
		{InstanceRejected, InstanceDetail{}, TaskRejected, A2ARejected},
	}
}

// StateProjection retains both axes so an adapter cannot lose paused, detached,
// orphaned, approval waiting or stopping semantics when projecting to A2A.
type StateProjection struct {
	Instance InstanceStatus `json:"instance_status"`
	Detail   InstanceDetail `json:"instance_detail"`
	Session  SessionState   `json:"session_state"`
	Task     TaskState      `json:"task_state"`
	A2A      A2AState       `json:"a2a_state"`
}

// ProjectState is only a reporting projection, not a task/instance transition
// engine. Existing per-task TaskState records remain authoritative for that task.
func ProjectState(status InstanceStatus, detail InstanceDetail, session SessionState) (StateProjection, error) {
	if !session.Valid() {
		return StateProjection{}, fmt.Errorf("invalid session state %q", session)
	}
	for _, row := range InstanceStateMappings() {
		if row.Status != status || row.Detail != detail {
			continue
		}
		result := StateProjection{Instance: status, Detail: detail, Session: session, Task: row.Task, A2A: row.A2A}
		if (session == SessionPaused || session == SessionOrphaned) && (row.Task == TaskSubmitted || row.Task == TaskWorking) {
			result.Task = TaskPaused
			result.A2A = A2AWorking
		}
		return result, nil
	}
	return StateProjection{}, fmt.Errorf("invalid instance status/detail %q %+v", status, detail)
}
