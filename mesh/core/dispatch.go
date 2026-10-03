package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	TaskLookup            Verb = "task.lookup"
	EventFollow           Verb = "event.follow"
	DispatchCapabilityURI      = "urn:hollis-labs:mesh:dispatch/v1"
	ResultSchemaV1             = "urn:hollis-labs:mesh:result/v1"
	MaxReplayPageSize          = 128
)

// Diagnostic identifies a refusal or uncertainty without parsing human prose.
type Diagnostic string

const (
	DiagnosticUnsupportedRequirement Diagnostic = "unsupported_requirement"
	DiagnosticInvalidPin             Diagnostic = "invalid_pin"
	DiagnosticStaleSnapshot          Diagnostic = "stale_snapshot"
	DiagnosticTargetBusy             Diagnostic = "target_busy"
	DiagnosticBudgetExhausted        Diagnostic = "budget_exhausted"
	DiagnosticAmbiguousAdmission     Diagnostic = "ambiguous_admission"
	DiagnosticReplayGap              Diagnostic = "replay_gap"
	DiagnosticProviderUnavailable    Diagnostic = "provider_unavailable"
)

// DispatchError wraps the stable error code with machine-readable scope and repair
// guidance. Ambiguous admission never proves refusal: recover the original intent.
type DispatchError struct {
	Cause      *Error     `json:"cause"`
	Diagnostic Diagnostic `json:"diagnostic"`
	Scope      string     `json:"scope,omitempty"`
	Repair     string     `json:"repair,omitempty"`
}

func (e *DispatchError) Error() string {
	if e == nil {
		return "dispatch error"
	}
	if e.Cause == nil {
		return string(e.Diagnostic)
	}
	return e.Cause.Error()
}
func (e *DispatchError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return nil
	}
	return e.Cause
}
func NewDispatchError(code ErrorCode, diagnostic Diagnostic, scope, repair string) error {
	return &DispatchError{Cause: &Error{Code: code, Message: string(diagnostic)}, Diagnostic: diagnostic, Scope: scope, Repair: repair}
}

type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryDelivered DeliveryStatus = "delivered"
)

// RosterProvenance retains the selected membership at admission. ProviderVersion
// and host StoreVersion belong to different counters and must never be compared.
type RosterProvenance struct {
	Team            URN    `json:"team"`
	Member          URN    `json:"member"`
	Membership      Member `json:"membership"`
	ProviderVersion uint64 `json:"provider_version"`
	StoreVersion    string `json:"store_version,omitempty"`
}

// AssignmentReceipt is published atomically with the admitted task and delivery
// journal. Admission says nothing about readiness or business result approval.
// IntentKey is scoped to Caller. Retries retain selection and immutable digest;
// lookup returns current state rather than implying the original response is fresh.
type AssignmentReceipt struct {
	TaskURN        URN               `json:"task_urn"`
	Caller         Actor             `json:"caller"`
	IntentKey      string            `json:"intent_key"`
	RequestDigest  string            `json:"request_digest"`
	AcceptedTarget URN               `json:"accepted_target"`
	ResolvedMember URN               `json:"resolved_member"`
	Roster         *RosterProvenance `json:"roster,omitempty"`
	Delivery       DeliveryStatus    `json:"delivery"`
	State          TaskState         `json:"state"`
	ActorURN       URN               `json:"actor_urn,omitempty"`
	SessionURN     URN               `json:"session_urn,omitempty"`
	InstanceID     string            `json:"instance_id,omitempty"`
	Result         *VersionedResult  `json:"result,omitempty"`
}

// VersionedResult binds schema, media type and exact bytes. Unknown schemas must
// remain unresolved evidence; they cannot transition a task to completed.
type VersionedResult struct {
	SchemaVersion string `json:"schema_version"`
	ContentType   string `json:"content_type"`
	Digest        string `json:"digest"`
	Content       []byte `json:"content"`
}

func ContentDigest(b []byte) string {
	d := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(d[:])
}
func (r VersionedResult) Validate() error {
	if r.SchemaVersion != ResultSchemaV1 || r.ContentType != "application/json" {
		return NewDispatchError(ErrorUnsupported, DiagnosticUnsupportedRequirement, "result", "negotiate a supported result schema and content type")
	}
	if !json.Valid(r.Content) || r.Digest != ContentDigest(r.Content) {
		return NewError(ErrorInvalid, "invalid result content or digest")
	}
	return nil
}

// AssignmentDigest covers canonical JSON encoding of the portable request,
// including actor, key, constraints, limits, history, target and lineage.
// CorrelationID is excluded because it is trace metadata, not work identity.
// All other fields are retained; callers keep immutable work content on retries.
func AssignmentDigest(r Request) (string, error) {
	r.CorrelationID = ""
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return ContentDigest(b), nil
}

// LookupRequest selects exactly one task URN or an intent scoped to the caller.
type LookupRequest struct {
	TaskURN   URN    `json:"task_urn,omitempty"`
	IntentKey string `json:"intent_key,omitempty"`
}

// LogPosition cursors are opaque, provider/log-scoped bookmarks. Snapshot and
// watermark are observed atomically; Head and Oldest describe retention at read.
type LogPosition struct {
	Log       URN    `json:"log"`
	Watermark string `json:"watermark"`
	Head      string `json:"head"`
	Oldest    string `json:"oldest"`
}
type TaskSnapshot struct {
	Receipt  AssignmentReceipt `json:"receipt"`
	Task     Task              `json:"task"`
	Position LogPosition       `json:"position"`
}

// FollowRequest replays after Cursor (empty starts at retained history), bounded
// by Limit. At the head, Wait tails until an authorized matching event or context
// cancellation. Grants are checked on every read. Subjects/Kinds narrow visibility
// but never confer it; private events are masked before crossing the boundary.
type FollowRequest struct {
	Log      URN      `json:"log"`
	Cursor   string   `json:"cursor,omitempty"`
	Limit    int      `json:"limit"`
	Subjects []URN    `json:"subjects,omitempty"`
	Kinds    []string `json:"kinds,omitempty"`
	Wait     bool     `json:"wait,omitempty"`
}

// ReplayPage.Next advances across masked/filtered events as well as visible ones.
// Gap is explicit evidence loss, with no replayed events and Next unchanged
// from the requested cursor; reconcile a snapshot
// before restarting at its watermark. HasMore refers to this atomic page's head.
type ReplayPage struct {
	Events     []Event     `json:"events"`
	Position   LogPosition `json:"position"`
	Next       string      `json:"next"`
	HasMore    bool        `json:"has_more"`
	Gap        bool        `json:"gap"`
	Diagnostic Diagnostic  `json:"diagnostic,omitempty"`
}

// AdmissionConstraints carry immutable requirements into the keyed request.
// Definition pins are verified by the authoritative host before admission.
// ExpectedProviderRosterVersion refers solely to the provider roster counter.
// HostStoreVersion is a separately named provenance requirement; a provider that
// cannot verify it must refuse instead of comparing it to its own counter.
type AdmissionConstraints struct {
	Definition                    *DefinitionRef `json:"definition,omitempty"`
	Requirements                  []Requirement  `json:"requirements,omitempty"`
	ExpectedProviderRosterVersion uint64         `json:"expected_provider_roster_version,omitempty"`
	HostStoreVersion              string         `json:"host_store_version,omitempty"`
}
