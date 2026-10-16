package runtimeevents

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the on-the-wire version of the [Event] envelope. Bump
// when the envelope shape changes in a non-additive way. Adding new
// [EventKind] values, new [SourceChannel] values, or new optional fields
// does not require a bump.
const SchemaVersion = "1"

// Event is the runtime activity event envelope.
//
// Producers must populate SchemaVersion, ID, Kind, Time, Sequence, and
// Source.Channel, and at least one of SessionID or Subject. SessionID is
// required for every session-bound kind (see [Event.Validate]); events that
// are not tied to a session (daemon, fleet, message, task) carry Subject
// instead. All other fields are optional and may be omitted when not
// meaningful for the event.
//
// Envelope v1.1 adds optional nested blocks (Scope, Corr, Actor, Class) and
// log-assigned fields (RecordedAt, Cursor). They are additive: SchemaVersion
// stays "1" and a v1 consumer ignores them. Field names follow mesh.Event so
// [ToMesh] and [FromMesh] project between the two envelopes without loss.
//
// Payload is opaque to this package; per-kind payload shapes are defined
// by the wrapper, by adapters, or by consuming apps. Apps that need to
// inspect a payload should switch on Kind and unmarshal Payload into the
// app-local type for that kind.
type Event struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	Kind          EventKind `json:"kind"`
	Time          time.Time `json:"time"`

	App       string `json:"app,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Sequence  uint64 `json:"sequence"`

	// ParentID correlates request/response pairs (a response carries the
	// request event's ID) and chains derived events back to their source.
	ParentID string `json:"parent_id,omitempty"`

	// RawOffset, when set, is the byte offset into the per-session raw log
	// where this event's content begins. Used for byte-level replay of
	// stdin/stdout/stderr streams. Pointer so a zero offset is
	// distinguishable from "unset".
	RawOffset *int64 `json:"raw_offset,omitempty"`

	Process Process         `json:"process"`
	Source  Source          `json:"source"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// Subject names what the event is about when it is not a session: a
	// URN such as msg://task/torque/CW-20261016-0129. Required when
	// SessionID is empty.
	Subject string `json:"subject,omitempty"`

	// Generation is the producer's restart counter: it changes whenever the
	// producer restarts its Sequence. Zero means unknown and is read as 1.
	Generation uint64 `json:"generation,omitempty"`

	// RecordedAt and Cursor are assigned by the durable log on append.
	// Producers leave them empty. Cursor is opaque and is the only value a
	// consumer may resume from; Sequence stays inside its producer.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	Cursor     string     `json:"cursor,omitempty"`

	// ContentType describes Payload (default application/json). Truncated
	// reports that the producer cut Payload to fit a size cap.
	ContentType string `json:"content_type,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`

	Scope *Scope `json:"scope,omitempty"`
	Corr  *Corr  `json:"corr,omitempty"`
	Actor *Actor `json:"actor,omitempty"`
	Class *Class `json:"class,omitempty"`

	// PayloadDigest marks an event whose payload a retention pass removed
	// (event retention D7, envelope tier). It is "sha256:" plus 64 lowercase
	// hex digits over the stored payload bytes, or "withheld" when no digest
	// is kept (PII). When it is set Payload is absent. Producers never set
	// it. See [Event.Tombstoned].
	PayloadDigest string `json:"payload_digest,omitempty"`
}

// Tombstoned reports whether a retention pass removed the event's payload.
func (e Event) Tombstoned() bool { return e.PayloadDigest != "" }

// Process describes the subprocess the event pertains to. Zero values mean
// "unknown" or "not yet assigned" — e.g., a plant.started event emitted
// before the process spawns will have PID == 0.
type Process struct {
	PID int `json:"pid,omitempty"`

	// Provider is the upstream agent identity ("claude", "codex",
	// "opencode", ...). Open string — apps may use provider names this
	// package does not enumerate.
	Provider string `json:"provider,omitempty"`

	// Runtime is the transport the wrapper selected for this provider
	// ("pty", "streaming-stdio", "jsonrpc-stdio", "http-sse", ...).
	Runtime string `json:"runtime,omitempty"`

	// ProviderSessionID is the provider's own session identity (e.g., a
	// Claude session UUID), when the provider exposes one. SessionID at
	// the [Event] level always refers to the wrapper's session identity.
	ProviderSessionID string `json:"provider_session_id,omitempty"`
}

// Source describes how the event was observed. Channel identifies the
// observation transport; Confidence describes how directly the underlying
// signal maps to the event semantics.
type Source struct {
	Channel    SourceChannel `json:"channel"`
	Confidence Confidence    `json:"confidence,omitempty"`
}
