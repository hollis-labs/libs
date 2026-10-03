package mesh

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

// URN is a minted actor or resource address. Actor addresses use msg://.
type URN string

func (u URN) Validate() error {
	p, err := url.Parse(string(u))
	if err != nil || (p.Scheme != "msg" && p.Scheme != "urn") || (p.Scheme == "msg" && (p.Host == "" || strings.Trim(p.Path, "/") == "")) {
		return fmt.Errorf("invalid URN %q", u)
	}
	return nil
}

type ActorKind string

const (
	ActorAgent   ActorKind = "agent"
	ActorUser    ActorKind = "user"
	ActorService ActorKind = "service"
	ActorTool    ActorKind = "tool"
)

type Actor struct {
	URN  URN       `json:"urn"`
	Kind ActorKind `json:"kind"`
}

func (a Actor) Validate() error {
	if err := a.URN.Validate(); err != nil {
		return err
	}
	switch a.Kind {
	case ActorAgent, ActorUser, ActorService, ActorTool:
	default:
		return fmt.Errorf("invalid actor kind %q", a.Kind)
	}
	p, _ := url.Parse(string(a.URN))
	if p.Scheme != "msg" || p.Host != string(a.Kind) {
		return fmt.Errorf("actor address does not match kind")
	}
	return nil
}

type Verb string

const (
	Describe         Verb = "describe"
	Negotiate        Verb = "negotiate"
	AgentLaunch      Verb = "agent.launch"
	AgentStop        Verb = "agent.stop"
	AgentResume      Verb = "agent.resume"
	AgentStatus      Verb = "agent.status"
	AgentList        Verb = "agent.list"
	TeamForm         Verb = "team.form"
	TeamDissolve     Verb = "team.dissolve"
	MemberAdd        Verb = "member.add"
	MemberRemove     Verb = "member.remove"
	MemberJoin       Verb = "member.join"
	MemberLeave      Verb = "member.leave"
	RoleAssign       Verb = "role.assign"
	Delegate         Verb = "delegate"
	Handoff          Verb = "handoff"
	Assign           Verb = "assign"
	MessageSend      Verb = "message.send"
	MessageAddress   Verb = "message.address"
	MessageBroadcast Verb = "message.broadcast"
	ChannelPublish   Verb = "channel.publish"
	ChannelSubscribe Verb = "channel.subscribe"
	Reply            Verb = "reply"
	Steer            Verb = "steer"
	Interrupt        Verb = "interrupt"
	RequestInput     Verb = "request_input"
	RequestApproval  Verb = "request_approval"
	RequestAuth      Verb = "request_auth"
	Approve          Verb = "approve"
	Reject           Verb = "reject"
	SelectNext       Verb = "select_next"
	Checkpoint       Verb = "checkpoint"
	Resume           Verb = "resume"
	Cancel           Verb = "cancel"
	Terminate        Verb = "terminate"
	ReportProgress   Verb = "report_progress"
	ReportResult     Verb = "report_result"
	EmitArtifact     Verb = "emit_artifact"
	PhaseSignal      Verb = "phase.signal"
	PhaseAdvance     Verb = "phase.advance"
)

type TaskState string

const (
	TaskSubmitted     TaskState = "submitted"
	TaskWorking       TaskState = "working"
	TaskInputRequired TaskState = "input_required"
	TaskAuthRequired  TaskState = "auth_required"
	TaskPaused        TaskState = "paused"
	TaskCompleted     TaskState = "completed"
	TaskFailed        TaskState = "failed"
	TaskCanceled      TaskState = "canceled"
	TaskRejected      TaskState = "rejected"
)

func (s TaskState) Valid() bool {
	switch s {
	case TaskSubmitted, TaskWorking, TaskInputRequired, TaskAuthRequired, TaskPaused, TaskCompleted, TaskFailed, TaskCanceled, TaskRejected:
		return true
	}
	return false
}
func (s TaskState) Terminal() bool {
	switch s {
	case TaskCompleted, TaskFailed, TaskCanceled, TaskRejected:
		return true
	}
	return false
}

type SessionState string

const (
	SessionStarting SessionState = "starting"
	SessionRunning  SessionState = "running"
	SessionDetached SessionState = "detached"
	SessionPaused   SessionState = "paused"
	SessionOrphaned SessionState = "orphaned"
	SessionEnded    SessionState = "ended"
)

func (s SessionState) Valid() bool {
	switch s {
	case SessionStarting, SessionRunning, SessionDetached, SessionPaused, SessionOrphaned, SessionEnded:
		return true
	}
	return false
}

type HistoryPolicy string

const (
	HistoryFull     HistoryPolicy = "full"
	HistorySummary  HistoryPolicy = "summary"
	HistoryFiltered HistoryPolicy = "filtered"
	HistoryNone     HistoryPolicy = "none"
)

func (h HistoryPolicy) Valid() bool {
	switch h {
	case HistoryFull, HistorySummary, HistoryFiltered, HistoryNone:
		return true
	}
	return false
}

// Effective returns the default summary policy when unspecified.
func (h HistoryPolicy) Effective() HistoryPolicy {
	if h == "" {
		return HistorySummary
	}
	return h
}

// Limits are resolved by hosts from request, team policy, then provider defaults.
// Zero means unspecified, never unlimited. Budget is in host-defined units.
type Limits struct {
	MaxDepth    int           `json:"max_depth"`
	MaxChildren int           `json:"max_children"`
	FanOut      int           `json:"fan_out"`
	MaxRounds   int           `json:"max_rounds,omitempty"`
	MaxStalls   int           `json:"max_stalls,omitempty"`
	Budget      float64       `json:"budget"`
	Timeout     time.Duration `json:"timeout"`
}

func (l Limits) Validate() error {
	if l.MaxDepth <= 0 || l.MaxChildren <= 0 || l.FanOut <= 0 || l.Budget <= 0 || math.IsNaN(l.Budget) || math.IsInf(l.Budget, 0) || l.Timeout <= 0 || l.MaxRounds < 0 || l.MaxStalls < 0 {
		return fmt.Errorf("spawn limits must be positive (rounds/stalls may be unspecified)")
	}
	return nil
}

// DefinitionRef pins an immutable semantic revision. Digest, when supplied,
// identifies the semantic content; verification belongs to the resolver host.
type DefinitionRef struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Digest   string `json:"digest,omitempty"`
}

// Verbs returns the complete mesh vocabulary, independent of provider support.
// Callers receive their own copy; capability descriptors claim a subset.
func Verbs() []Verb {
	return []Verb{
		Describe,
		Negotiate,
		AgentLaunch,
		AgentStop,
		AgentResume,
		AgentStatus,
		AgentList,
		TeamForm,
		TeamDissolve,
		MemberAdd,
		MemberRemove,
		MemberJoin,
		MemberLeave,
		RoleAssign,
		Delegate,
		Handoff,
		Assign,
		MessageSend,
		MessageAddress,
		MessageBroadcast,
		ChannelPublish,
		ChannelSubscribe,
		Reply,
		Steer,
		Interrupt,
		RequestInput,
		RequestApproval,
		RequestAuth,
		Approve,
		Reject,
		SelectNext,
		Checkpoint,
		Resume,
		Cancel,
		Terminate,
		ReportProgress,
		ReportResult,
		EmitArtifact,
		PhaseSignal,
		PhaseAdvance,
	}
}
