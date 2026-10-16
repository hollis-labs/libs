package ledger

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Stage names who or what made the decision.
type Stage string

const (
	// StageRule is a declarative rule match.
	StageRule Stage = "rule"
	// StageClassifier is a fast closed-set classifier.
	StageClassifier Stage = "classifier"
	// StageModel is a model call.
	StageModel Stage = "model"
	// StageHuman is a person's decision.
	StageHuman Stage = "human"
	// StageReflex is a steering reflex.
	StageReflex Stage = "reflex"
	// StagePolicy is a policy decision point.
	StagePolicy Stage = "policy"
)

func (s Stage) valid() bool {
	switch s {
	case StageRule, StageClassifier, StageModel, StageHuman, StageReflex, StagePolicy:
		return true
	}
	return false
}

// versioned stages must say which rule set or model produced the output.
func (s Stage) versioned() bool {
	return s == StageRule || s == StageClassifier || s == StageModel || s == StageReflex || s == StagePolicy
}

// Disposition is where a decision stands.
type Disposition string

const (
	// Suggested means the decision awaits acceptance.
	Suggested Disposition = "suggested"
	// Applied means the decision was acted on.
	Applied Disposition = "applied"
	// Accepted means a person accepted the suggestion.
	Accepted Disposition = "accepted"
	// Rejected means the decision was declined.
	Rejected Disposition = "rejected"
	// Superseded means a newer decision replaced this one.
	Superseded Disposition = "superseded"
)

func (d Disposition) valid() bool {
	switch d {
	case Suggested, Applied, Accepted, Rejected, Superseded:
		return true
	}
	return false
}

// canMove: a suggestion resolves any way; every other state can only be
// superseded (by a newer decision on the same subject).
func canMove(from, to Disposition) bool {
	switch {
	case from == Superseded || from == to:
		return false
	case from == Suggested:
		return to.valid()
	}
	return to == Superseded
}

// InputMode is how a decision's input is kept.
type InputMode string

const (
	ModeSnapshot InputMode = "snapshot" // the input itself, subject to retention
	ModePointer  InputMode = "pointer"  // a ref to where the input lives
	ModeNone     InputMode = "none"     // hash only
)

// Input is what the decision saw. Hash is always present (over the original
// bytes, or HMAC'd when Retention.HashKey is set); the snapshot may expire.
type Input struct {
	Mode         InputMode       `json:"mode"`
	Snapshot     json.RawMessage `json:"snapshot,omitempty"`
	Pointer      string          `json:"pointer,omitempty"`
	Hash         string          `json:"hash"`
	Redacted     bool            `json:"redacted,omitempty"`
	Keep         bool            `json:"keep,omitempty"` // training-set flag: never expire
	RawExpiresAt *time.Time      `json:"raw_expires_at,omitempty"`
	Expired      bool            `json:"expired,omitempty"`
}

// Version identifies the rule set or model. Hash pins exact content; Label is
// the human-facing version string.
type Version struct {
	Name  string `json:"name,omitempty"`
	Label string `json:"label"`
	Hash  string `json:"hash,omitempty"`
}

// Output is what the decision produced.
type Output struct {
	Label         string             `json:"label"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Detail        json.RawMessage    `json:"detail,omitempty"`
}

// Alternative is something that was considered and why it was or was not chosen.
type Alternative struct {
	Label   string `json:"label"`
	Matched bool   `json:"matched"`
	Why     string `json:"why,omitempty"`
}

// Envelope joins the row to the runtime-events correlation ids.
type Envelope struct {
	SessionID    string `json:"session_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	ParentID     string `json:"parent_id,omitempty"`
	WorkstreamID string `json:"workstream_id,omitempty"`
	TraceID      string `json:"trace_id,omitempty"`
}

// Record is one decision. Immutable once appended.
type Record struct {
	DecisionID     string        `json:"decision_id"`
	IdempotencyKey string        `json:"idempotency_key"`
	Stage          Stage         `json:"stage"`
	SubjectRef     string        `json:"subject_ref"`
	Input          Input         `json:"input"`
	Version        Version       `json:"version"`
	Output         Output        `json:"output"`
	Alternatives   []Alternative `json:"alternatives_considered,omitempty"`
	Disposition    Disposition   `json:"disposition"`
	Envelope       Envelope      `json:"envelope"`
	CreatedAt      time.Time     `json:"created_at"`
}

// Validate checks shape, not meaning.
func (r Record) Validate() error {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	if !r.Stage.valid() {
		bad("unknown stage %q", r.Stage)
	}
	if strings.TrimSpace(r.SubjectRef) == "" {
		bad("subject_ref required")
	}
	if r.IdempotencyKey == "" {
		bad("idempotency_key required (use ledger.Key)")
	}
	if !r.Disposition.valid() {
		bad("unknown disposition %q", r.Disposition)
	}
	if r.Stage.versioned() && r.Version.Label == "" {
		bad("stage %s needs a version label", r.Stage)
	}
	if r.Input.Hash == "" {
		bad("input hash required (use Retention.Shape)")
	}
	switch r.Input.Mode {
	case ModeSnapshot:
		if len(r.Input.Snapshot) == 0 && !r.Input.Expired {
			bad("snapshot mode without a snapshot")
		}
	case ModePointer:
		if r.Input.Pointer == "" {
			bad("pointer mode without a pointer")
		}
	case ModeNone:
	default:
		bad("unknown input mode %q", r.Input.Mode)
	}
	if r.Output.Label == "" {
		bad("output label required")
	}
	if c := r.Output.Confidence; c != nil && (*c < 0 || *c > 1) {
		bad("confidence outside [0,1]")
	}
	for k, p := range r.Output.Probabilities {
		if p < 0 || p > 1 {
			bad("probability for %q outside [0,1]", k)
		}
	}
	return errors.Join(errs...)
}

// Key is the idempotency key: one decision per subject, stage and version.
// Re-running the same stage on the same subject with the same rule set or model
// yields the same key, so a retry or a replay appends nothing new.
func Key(subjectRef string, stage Stage, v Version) string {
	h := sha256.New()
	for _, p := range []string{subjectRef, string(stage), v.Label, v.Hash} {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewID returns a random decision id.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "dec_" + hex.EncodeToString(b[:])
}

// HashInput hashes input bytes; with a key it is an HMAC, so a low-entropy input
// (a short answer, an email address) cannot be brute-forced from the hash.
func HashInput(raw, key []byte) string {
	if len(key) > 0 {
		m := hmac.New(sha256.New, key)
		m.Write(raw)
		return hex.EncodeToString(m.Sum(nil))
	}
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

// --- events ---

// EventKind is the type of a ledger event.
type EventKind string

const (
	// EventDisposition records a disposition change.
	EventDisposition EventKind = "disposition"
	// EventCorrection records a human correction.
	EventCorrection EventKind = "correction"
	// EventOutcome records what happened downstream.
	EventOutcome EventKind = "outcome"
)

// Correction is a human's label for the decision's subject. It is a training
// label in its own right and does not rewrite the original output.
type Correction struct {
	Label string `json:"label"`
	Actor string `json:"actor"`
	Note  string `json:"note,omitempty"`
}

// Outcome links the decision to what happened downstream.
type Outcome struct {
	Result string `json:"result"` // success | failure | unknown
	Ref    string `json:"ref"`    // later event / run / delivery reference
}

// Event is one append-only entry about a decision.
type Event struct {
	DecisionID  string      `json:"decision_id"`
	Seq         int         `json:"seq"`
	Kind        EventKind   `json:"kind"`
	At          time.Time   `json:"at"`
	Disposition Disposition `json:"disposition,omitempty"`
	Correction  *Correction `json:"correction,omitempty"`
	Outcome     *Outcome    `json:"outcome,omitempty"`
}

func (e Event) validate() error {
	if e.DecisionID == "" {
		return errors.New("decision_id required")
	}
	switch e.Kind {
	case EventDisposition:
		if !e.Disposition.valid() {
			return errors.New("disposition event needs a valid disposition")
		}
	case EventCorrection:
		if e.Correction == nil || e.Correction.Label == "" || strings.TrimSpace(e.Correction.Actor) == "" {
			return errors.New("correction needs a label and an actor")
		}
	case EventOutcome:
		if e.Outcome == nil || e.Outcome.Ref == "" {
			return errors.New("outcome needs a ref")
		}
		switch e.Outcome.Result {
		case "success", "failure", "unknown":
		default:
			return errors.New("outcome result must be success, failure or unknown")
		}
	default:
		return fmt.Errorf("unknown event kind %q", e.Kind)
	}
	return nil
}

// Entry is a row with its events.
type Entry struct {
	Record Record  `json:"record"`
	Events []Event `json:"events,omitempty"`
}

// Current is the disposition after all events.
func (e Entry) Current() Disposition {
	d := e.Record.Disposition
	for _, ev := range e.Events {
		if ev.Kind == EventDisposition {
			d = ev.Disposition
		}
	}
	return d
}

// Label is the training label: the latest human correction, else the original
// output. Corrected reports which.
func (e Entry) Label() (label string, corrected bool) {
	label = e.Record.Output.Label
	for _, ev := range e.Events {
		if ev.Kind == EventCorrection {
			label, corrected = ev.Correction.Label, true
		}
	}
	return
}

// Outcome is the latest outcome event, if any.
func (e Entry) Outcome() (Outcome, bool) {
	var o Outcome
	var ok bool
	for _, ev := range e.Events {
		if ev.Kind == EventOutcome {
			o, ok = *ev.Outcome, true
		}
	}
	return o, ok
}

// Filter selects entries in Store.Query.
type Filter struct {
	Stage      Stage
	SubjectRef string
	Corrected  *bool // true: only corrected; false: only uncorrected; nil: both
	Since      time.Time
	Limit      int // 0 means 100
}

var (
	// ErrNotFound is returned for an unknown decision id.
	ErrNotFound = errors.New("ledger: not found")
	// ErrTransition is returned for a disposition move the lifecycle does not allow.
	ErrTransition = errors.New("ledger: disposition transition not allowed")
	// ErrInvalid is returned (wrapped) for a record or event that fails validation.
	ErrInvalid = errors.New("ledger: invalid")
)

// Store is the ledger's persistence. Append is idempotent on IdempotencyKey: a
// second append returns the stored record and duplicate=true, and writes nothing.
// Events are append-only and numbered per decision from 1.
type Store interface {
	Append(ctx context.Context, r Record) (stored Record, duplicate bool, err error)
	AppendEvent(ctx context.Context, e Event) (Event, error)
	Get(ctx context.Context, decisionID string) (Entry, error)
	Query(ctx context.Context, f Filter) ([]Entry, error)
}

// Expirer is implemented by stores that can drop expired raw snapshots,
// keeping the hash and every other field.
type Expirer interface {
	ExpireInputs(ctx context.Context, now time.Time) (int, error)
}
