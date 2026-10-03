package teams

import (
	"errors"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

var (
	ErrNotFound        = errors.New("teams: not found")
	ErrConflict        = errors.New("teams: conflicting request or version")
	ErrDenied          = errors.New("teams: authority denied")
	ErrUnavailable     = errors.New("teams: target unavailable")
	ErrLaunchFailed    = errors.New("teams: launch failed")
	ErrProvisionFailed = errors.New("teams: terminal provisioning failure")
	ErrUnsupported     = errors.New("teams: unsupported in v0")
)

type Resolution string

const (
	Durable Resolution = "durable"
	Pool    Resolution = "pool"
	Fresh   Resolution = "fresh"
)

type Activation string

const (
	Singleton    Activation = "singleton"
	FreshPerWake Activation = "fresh-per-wake"
	Concurrent   Activation = "concurrent"
)

type Dispatch string

const (
	IdleFirst  Dispatch = "idle-first"
	RoundRobin Dispatch = "round-robin"
	First      Dispatch = "first"
)

type Governance string

const (
	Owner      Governance = "owner"
	Admin      Governance = "admin"
	MemberRole Governance = "member"
)

type AuthorityMode string

const (
	Strict  AuthorityMode = "strict"
	DevOpen AuthorityMode = "dev-open"
)

type Permission string

const (
	MaySpawn       Permission = "may_spawn"
	MayMessage     Permission = "may_message"
	MayDelegate    Permission = "may_delegate"
	MayHandoff     Permission = "may_handoff"
	MayAssign      Permission = "may_assign"
	MayApprove     Permission = "may_approve"
	MaySignalPhase Permission = "may_signal_phase"
	MayAdmin       Permission = "may_admin"
	MayNotReview   Permission = "may_not_review"
	Self                      = "self"
)

type Team struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   uint64    `json:"version"`
	Slots     []Slot    `json:"slots"`
	Phases    []Phase   `json:"phases"`
	Authority Authority `json:"authority"`
	Routing   Routing   `json:"routing"`
	Policy    Policy    `json:"policy"`
}
type Slot struct {
	Name       string             `json:"name"`
	Role       string             `json:"role"`
	Definition mesh.DefinitionRef `json:"definition_ref"`
	Resolution Resolution         `json:"resolution"`
	Identity   mesh.URN           `json:"identity,omitempty"`
	Identities []mesh.URN         `json:"identities,omitempty"`
	Pool       string             `json:"pool,omitempty"`
	Activation Activation         `json:"activation"`
	Required   bool               `json:"required"`
	Min        int                `json:"min"`
	Max        int                `json:"max"`
	Dispatch   Dispatch           `json:"dispatch,omitempty"`
	Workspace  map[string]string  `json:"workspace,omitempty"`
}
type Phase struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	ActiveSlots  []string `json:"active_slots"`
	OwnerSlot    string   `json:"owner_slot,omitempty"`
	ExitTrigger  Trigger  `json:"exit_trigger"`
	ApproverSlot string   `json:"approver_slot,omitempty"`
}

// Spec is the host trigger vocabulary, not a second condition language.
type Trigger struct {
	Kind           string            `json:"kind"`
	Spec           map[string]string `json:"spec"`
	AuthorizedSlot string            `json:"authorized_slot,omitempty"`
	Auto           bool              `json:"auto,omitempty"`
}
type Authority struct {
	Mode   AuthorityMode `json:"mode"`
	Grants []Grant       `json:"grants"`
}

// A grant names either a slot or an actor; actor-specific grants don't
// depend on actor kind. ToSlot=self refers to the same actor, not peers.
type Grant struct {
	FromSlot  string     `json:"from_slot,omitempty"`
	FromActor mesh.URN   `json:"from_actor,omitempty"`
	Verb      Permission `json:"verb"`
	ToSlot    string     `json:"to_slot,omitempty"`
	ToActor   mesh.URN   `json:"to_actor,omitempty"`
}
type Routing struct {
	Rules           []RoutingRule `json:"rules"`
	CoordinatorSlot string        `json:"coordinator_slot,omitempty"`
}
type RoutingRule struct {
	Name       string   `json:"name"`
	Phrases    []string `json:"phrases"`
	TargetSlot string   `json:"target_slot"`
	Priority   int      `json:"priority,omitempty"`
}
type Policy struct {
	Spawn   mesh.Limits        `json:"spawn"`
	History mesh.HistoryPolicy `json:"history,omitempty"`
}
type Member struct {
	ID              string            `json:"id"`
	Slot            string            `json:"slot"`
	Actor           mesh.URN          `json:"actor"`
	Kind            mesh.ActorKind    `json:"kind"`
	AgentID         string            `json:"agent_id,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	Status          string            `json:"status"`
	Governance      Governance        `json:"governance"`
	Resolution      Resolution        `json:"resolution"`
	JoinedAt        time.Time         `json:"joined_at"`
	Parent          string            `json:"parent,omitempty"`
	Limits          mesh.Limits       `json:"limits"`
	Enrolled        bool              `json:"enrolled"`
	Ephemeral       bool              `json:"ephemeral"`
	SpawnCapable    bool              `json:"spawn_capable"`
	Intent          *ProvisionRequest `json:"intent,omitempty"`
	ProvisionDigest string            `json:"provision_digest,omitempty"`
	Budget          float64           `json:"budget,omitempty"`
	Idle            bool              `json:"idle"`
}
type TeamRun struct {
	ID          string         `json:"id"` // The host's workflow run ID.
	TeamID      string         `json:"team_id"`
	TeamVersion uint64         `json:"team_version"`
	Channel     string         `json:"channel"`
	Status      mesh.TaskState `json:"status"`
}
type Roster struct {
	RunID   string   `json:"run_id"`
	Version uint64   `json:"version"`
	Members []Member `json:"members"`
}

func (t Team) slot(name string) (Slot, bool) {
	for _, s := range t.Slots {
		if s.Name == name {
			return s, true
		}
	}
	return Slot{}, false
}
func (r Roster) member(actor mesh.URN) (Member, error) {
	for _, m := range r.Members {
		if m.Actor == actor && m.Status == "active" {
			return m, nil
		}
	}
	return Member{}, ErrDenied
}

func (r Roster) knownMember(actor mesh.URN) (Member, error) {
	for _, m := range r.Members {
		if m.Actor == actor && (m.Status == "active" || m.Status == "stopped" || m.Status == "released") {
			return m, nil
		}
	}
	return Member{}, ErrDenied
}

// phase selects authored policy. API inputs select only an ID; callers may
// not replace owner, active slots, trigger, Auto or approver policy.
func (t Team) phase(id string) (Phase, error) {
	for _, p := range t.Phases {
		if p.ID == id {
			return clone(p), nil
		}
	}
	return Phase{}, ErrNotFound
}
