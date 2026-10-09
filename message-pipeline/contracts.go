package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

const (
	MaxEntries            = 16
	MaxIdentityCharacters = 128
	MaxSummaryCharacters  = 600
	MaxStateBytes         = 8 * 1024
)

var (
	ErrInvalid  = errors.New("pipeline: invalid contract")
	ErrConflict = errors.New("pipeline: immutable result conflict")
	ErrPending  = errors.New("pipeline: processing remains pending")
	ErrRefused  = errors.New("pipeline: stage refused")
)

type Disposition string

const (
	Pass Disposition = "pass"
	Hold Disposition = "hold"
	Drop Disposition = "drop"
)

type Outcome string

const (
	Passed   Outcome = "passed"
	Failed   Outcome = "failed"
	TimedOut Outcome = "timed_out"
)

type FailureCode string

const (
	StageError      FailureCode = "stage_error"
	StageTimeout    FailureCode = "stage_timeout"
	StagePanic      FailureCode = "stage_panic"
	StageRefused    FailureCode = "stage_refused"
	InvalidOutput   FailureCode = "invalid_output"
	AnnotationLimit FailureCode = "annotation_limit"
)

type FailMode string

const (
	FailOpen FailMode = "open"
	FailHold FailMode = "hold"
)

// Identity is opaque, exact source/message identity, not a body hash.
type Identity struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

// Attribution preserves asserted source labels without asserting authentication.
type Attribution struct {
	Sender    string `json:"sender"`
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	OutputID  string `json:"output_id,omitempty"`
}
type Message struct {
	Identity    Identity    `json:"identity"`
	Original    string      `json:"original"`
	Attribution Attribution `json:"attribution"`
}
type Summary struct {
	Text string `json:"text"`
}
type Annotation struct {
	SchemaVersion int     `json:"schema_version"`
	StageID       string  `json:"stage_id"`
	StageVersion  string  `json:"stage_version"`
	Kind          string  `json:"kind"`
	Summary       Summary `json:"summary"`
}
type Trace struct {
	StageID      string      `json:"stage_id"`
	StageVersion string      `json:"stage_version"`
	Outcome      Outcome     `json:"outcome"`
	DurationMS   int64       `json:"duration_ms"`
	FailureCode  FailureCode `json:"failure_code,omitempty"`
}
type State struct {
	Annotations []Annotation `json:"annotations"`
	Traces      []Trace      `json:"stage_trace"`
}

func (s State) Clone() State {
	s.Annotations = append([]Annotation(nil), s.Annotations...)
	s.Traces = append([]Trace(nil), s.Traces...)
	return s
}

// FollowUp is an inert declaration. No executor exists in this package.
// Payload kinds require a separate versioned contract; MVP declares none.
type FollowUp struct {
	Kind      string   `json:"kind"`
	Reference Identity `json:"reference"`
}
type Input struct {
	Message Message
	State   State
}
type Result struct {
	Summaries   []Summary
	Disposition Disposition
	FollowUps   []FollowUp
}

func (r Result) clone() Result {
	r.Summaries = append([]Summary(nil), r.Summaries...)
	r.FollowUps = append([]FollowUp(nil), r.FollowUps...)
	return r
}

type Stage interface {
	Run(context.Context, Input) (Result, error)
}
type StageFunc func(context.Context, Input) (Result, error)

func (f StageFunc) Run(ctx context.Context, in Input) (Result, error) { return f(ctx, in) }

// ConfigDigest and InstructionDigest must describe immutable owner snapshots.
// Priority ascends; configuration order breaks equal priorities.
type StageSpec struct {
	ID                string
	Version           string
	Priority          int
	Timeout           time.Duration
	FailMode          FailMode
	ConfigDigest      string
	InstructionDigest string
	Stage             Stage
}

// Key uses exact owner-provided identity and digests. Timeout/fail mode also
// participate so a changed execution contract cannot reuse an old failure.
type Key struct {
	Message           Identity `json:"message"`
	StageID           string   `json:"stage_id"`
	StageVersion      string   `json:"stage_version"`
	ConfigDigest      string   `json:"config_digest"`
	InstructionDigest string   `json:"instruction_digest"`
	TimeoutNS         int64    `json:"timeout_ns"`
	FailMode          FailMode `json:"fail_mode"`
}

// Record binds an outcome to the exact input including upstream annotations.
// InputDigest detects same-key changed input; it never substitutes message identity.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Result        Result `json:"result"`
	Trace         Trace  `json:"trace"`
}

func (r Record) Clone() Record { r.Result = r.Result.clone(); return r }

// ResultStore must durably insert before returning. PutIfAbsent returns the
// existing immutable record on contention. It must never overwrite an outcome.
// Calls are context-bounded. A store error leaves the message pending.
// This interface does not promise provider exactly-once across concurrent owners.
type ResultStore interface {
	Get(context.Context, Key) (Record, bool, error)
	PutIfAbsent(context.Context, Key, Record) (Record, error)
}

// Cursor is an opaque source checkpoint. Version permits conditional settlement.
// Sequence is source-owned ordering, never a timestamp or message count.
type Cursor struct {
	Sequence uint64
	Version  string
}

// CursorStore is adapter-owned. CompareAndSwap must not advance past unsettled
// publications. An adapter needing atomic sink/result/cursor settlement implements
// that transaction itself; Runner never advances a cursor.
type CursorStore interface {
	Load(context.Context, string) (Cursor, bool, error)
	CompareAndSwap(context.Context, string, Cursor, Cursor) (bool, error)
}
type Settlement struct {
	Message     Message
	State       State
	Disposition Disposition
	FollowUps   []FollowUp
}

func validText(s string, limit int, empty bool) bool {
	return utf8.ValidString(s) && (empty || s != "") && utf8.RuneCountInString(s) <= limit
}
func validIdentity(s string) bool { return validText(s, MaxIdentityCharacters, false) }
func validCode(c FailureCode) bool {
	switch c {
	case StageError, StageTimeout, StagePanic, StageRefused, InvalidOutput, AnnotationLimit:
		return true
	}
	return false
}
func (s State) Validate() error {
	if len(s.Annotations) > MaxEntries || len(s.Traces) > MaxEntries {
		return ErrInvalid
	}
	for _, a := range s.Annotations {
		if a.SchemaVersion != 1 || !validIdentity(a.StageID) || !validIdentity(a.StageVersion) || a.Kind != "summary" || !validText(a.Summary.Text, MaxSummaryCharacters, false) {
			return ErrInvalid
		}
	}
	for _, t := range s.Traces {
		if !validIdentity(t.StageID) || !validIdentity(t.StageVersion) || t.DurationMS < 0 {
			return ErrInvalid
		}
		switch t.Outcome {
		case Passed:
			if t.FailureCode != "" {
				return ErrInvalid
			}
		case Failed:
			if !validCode(t.FailureCode) || t.FailureCode == StageTimeout {
				return ErrInvalid
			}
		case TimedOut:
			if t.FailureCode != StageTimeout {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) > MaxStateBytes {
		return ErrInvalid
	}
	return nil
}
func digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("pipeline internal JSON: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
