// Package trace records experimental SDK evidence in the existing runtime event
// envelope. It does not export telemetry, authenticate authority, or execute a
// recorded operation. Replay produces detached evidence values only.
package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/usageledger"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

const Version = "1"

var ErrInvalid = errors.New("trace: invalid evidence")

type RunID string
type ModelID string
type OperationID string
type ApprovalID string
type DecisionID string

// Metadata contains optional host-supplied immutable evidence references, never
// grants. The recorder validates shape; it does not claim issuer verification.
type Metadata struct {
	Snapshot *runtimecontract.Reference `json:"snapshot,omitempty"`
	Build    *runtimecontract.Reference `json:"build,omitempty"`
}

type Correlation struct {
	RunID            RunID                      `json:"run_id"`
	Binding          runtimecontract.Binding    `json:"binding"`
	Environment      runtimecontract.Reference  `json:"environment"`
	DefinitionDigest string                     `json:"definition_digest"`
	Route            string                     `json:"route"`
	Model            ModelID                    `json:"model"`
	ModelCallID      sdk.ModelCallID            `json:"model_call_id,omitempty"`
	TurnID           runtimecontract.TurnID     `json:"turn_id,omitempty"`
	ToolCallID       runtimecontract.ToolCallID `json:"tool_call_id,omitempty"`
	Tool             string                     `json:"tool,omitempty"`
	OperationID      OperationID                `json:"operation_id,omitempty"`
	ApprovalID       ApprovalID                 `json:"approval_id,omitempty"`
	DecisionID       DecisionID                 `json:"decision_id,omitempty"`
}

// Position preserves different coordinate systems. Empty/zero values remain
// unknown; no cursor is manufactured from producer sequence or PTY bytes.
type Position struct {
	SourceSequence     runtimecontract.ProducerSequence   `json:"source_sequence"`
	ProducerGeneration runtimecontract.ProducerGeneration `json:"producer_generation,omitempty"`
	PTYOffset          *runtimecontract.PTYByteOffset     `json:"pty_offset,omitempty"`
	CommittedCursor    runtimecontract.CommittedCursor    `json:"committed_cursor,omitempty"`
}

type Kind string

const (
	Event     Kind = "event"
	Usage     Kind = "usage"
	Delivery  Kind = "delivery"
	Execution Kind = "execution"
)

// Entry has exactly one body matching Kind. Approval decisions remain event
// observations, delivery/effect receipts remain independently reported facts.
// A receipt or replayed event never becomes permission to act.
type Entry struct {
	Kind        Kind                   `json:"kind"`
	Correlation Correlation            `json:"correlation"`
	Position    Position               `json:"position"`
	Event       *runtimeevents.Event   `json:"event,omitempty"`
	Usage       *usageledger.Row       `json:"usage,omitempty"`
	Delivery    *gate.DeliveryReceipt  `json:"delivery,omitempty"`
	Execution   *gate.ExecutionReceipt `json:"execution,omitempty"`
}

type document struct {
	Version  string   `json:"version"`
	Metadata Metadata `json:"metadata"`
	Entries  []Entry  `json:"entries"`
}

// Recording stores only validated private bytes. All accessors and JSON output
// return detached copies; callers cannot mutate a retained recording.
type Recording struct{ raw []byte }

func (r Recording) Entries() []Entry { var d document; _ = json.Unmarshal(r.raw, &d); return d.Entries }
func (r Recording) Metadata() Metadata {
	var d document
	_ = json.Unmarshal(r.raw, &d)
	return d.Metadata
}
func (r Recording) MarshalJSON() ([]byte, error) {
	if len(r.raw) == 0 {
		return nil, ErrInvalid
	}
	return bytes.Clone(r.raw), nil
}
func (r *Recording) UnmarshalJSON(raw []byte) error {
	var d document
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	if d.Version != Version {
		return fmt.Errorf("%w: recording version", ErrInvalid)
	}
	recorder, err := NewRecorder(d.Metadata)
	if err != nil {
		return err
	}
	for _, entry := range d.Entries {
		if err = recorder.accept(context.Background(), entry); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		return err
	}
	r.raw = encoded
	return nil
}

// Replay returns in-order evidence only. It accepts no sink, callbacks, model,
// tool, runtime, approval or admission ports. It cannot resume or repeat effects,
// remap an old attempt to a successor, or resolve an unknown outcome.
func Replay(ctx context.Context, r Recording) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var validated Recording
	if err := validated.UnmarshalJSON(r.raw); err != nil {
		return nil, err
	}
	entries := validated.Entries()
	for range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// Recorder is a bounded concurrency-safe runtimeevents.Sink and gate.ReceiptSink.
// The caller controls durability by persisting a Snapshot; no store is created.
type Recorder struct {
	mu         sync.Mutex
	metadata   Metadata
	entries    []Entry
	maxEntries int
}

const MaxEntries = 10000

func NewRecorder(metadata Metadata) (*Recorder, error) {
	for _, ref := range []*runtimecontract.Reference{metadata.Snapshot, metadata.Build} {
		if ref != nil {
			if err := ref.Validate(); err != nil {
				return nil, err
			}
		}
	}
	detached, err := copyValue(metadata)
	if err != nil {
		return nil, err
	}
	return &Recorder{metadata: detached, maxEntries: MaxEntries}, nil
}
func (r *Recorder) Snapshot() (Recording, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := json.Marshal(document{Version: Version, Metadata: r.metadata, Entries: r.entries})
	return Recording{raw: raw}, err
}
func (r *Recorder) Write(ctx context.Context, event runtimeevents.Event) error {
	entry, err := eventEntry(event)
	if err != nil {
		return err
	}
	return r.accept(ctx, entry)
}

// RecordUsage accepts one final presence-aware row per observed model call.
// Rows are explicit adapter reports, not inferred from ambiguous legacy zeros or
// a JSON side channel in StreamEvent.Content. Prices are immutable snapshots.
func (r *Recorder) RecordUsage(ctx context.Context, c Correlation, row usageledger.Row) error {
	return r.accept(ctx, Entry{Kind: Usage, Correlation: c, Usage: &row})
}
func (r *Recorder) RecordDelivery(ctx context.Context, receipt gate.DeliveryReceipt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.entries) - 1; i >= 0; i-- {
		e := r.entries[i]
		if e.Correlation.Binding == receipt.Binding && string(e.Correlation.DecisionID) == receipt.DecisionRef {
			return r.appendLocked(ctx, Entry{Kind: Delivery, Correlation: e.Correlation, Delivery: &receipt})
		}
	}
	return fmt.Errorf("%w: delivery has no observed decision", ErrInvalid)
}
func (r *Recorder) RecordExecution(ctx context.Context, receipt gate.ExecutionReceipt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.entries) - 1; i >= 0; i-- {
		e := r.entries[i]
		if e.Correlation.Binding == receipt.Binding && string(e.Correlation.OperationID) == receipt.OperationID {
			return r.appendLocked(ctx, Entry{Kind: Execution, Correlation: e.Correlation, Execution: &receipt})
		}
	}
	return fmt.Errorf("%w: execution has no observed operation", ErrInvalid)
}
func (r *Recorder) accept(ctx context.Context, entry Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendLocked(ctx, entry)
}
func (r *Recorder) appendLocked(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(r.entries) >= r.maxEntries {
		return fmt.Errorf("%w: entry bound", ErrInvalid)
	}
	if err := validateEntry(e); err != nil {
		return err
	}
	if e.Kind != Event {
		found := false
		for _, observed := range r.entries {
			if observed.Kind == Event && observed.Correlation == e.Correlation {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w: supplemental evidence needs exact observed correlation", ErrInvalid)
		}
	}
	if e.Kind == Usage {
		if e.Correlation.ModelCallID == "" || e.Correlation.TurnID == "" {
			return fmt.Errorf("%w: usage needs model call", ErrInvalid)
		}
		for _, old := range r.entries {
			if old.Kind == Usage && old.Correlation.Binding == e.Correlation.Binding && old.Correlation.ModelCallID == e.Correlation.ModelCallID {
				return fmt.Errorf("%w: duplicate final model usage", ErrInvalid)
			}
		}
	}
	if e.Kind == Event && r.metadata.Snapshot != nil && r.metadata.Snapshot.OwnerID != e.Correlation.Binding.OwnerID {
		return fmt.Errorf("%w: snapshot owner", ErrInvalid)
	}
	detached, err := copyValue(e)
	if err != nil {
		return err
	}
	r.entries = append(r.entries, detached)
	return nil
}
func eventEntry(event runtimeevents.Event) (Entry, error) {
	if err := event.Validate(); err != nil {
		return Entry{}, err
	}
	if event.SchemaVersion != runtimeevents.SchemaVersion || !json.Valid(event.Payload) {
		return Entry{}, ErrInvalid
	}
	var p sdk.EventPayload
	decoder := json.NewDecoder(bytes.NewReader(event.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return Entry{}, err
	}
	c := Correlation{RunID: RunID(p.RunID), Binding: p.Binding, Environment: p.Environment, DefinitionDigest: p.DefinitionDigest, Route: p.Route, Model: ModelID(p.Model), ModelCallID: p.ModelCallID, TurnID: p.TurnID, ToolCallID: p.ToolCallID, Tool: p.Tool, OperationID: OperationID(p.OperationID), ApprovalID: ApprovalID(p.ApprovalID), DecisionID: DecisionID(p.DecisionID)}
	position := Position{SourceSequence: runtimecontract.ProducerSequence(event.Sequence), ProducerGeneration: runtimecontract.ProducerGeneration(event.Generation), CommittedCursor: runtimecontract.CommittedCursor(event.Cursor)}
	if event.RawOffset != nil {
		if *event.RawOffset < 0 {
			return Entry{}, ErrInvalid
		}
		offset := runtimecontract.PTYByteOffset(*event.RawOffset)
		position.PTYOffset = &offset
	}
	return Entry{Kind: Event, Correlation: c, Position: position, Event: &event}, nil
}
func validateEntry(e Entry) error {
	if strings.TrimSpace(string(e.Correlation.RunID)) == "" || e.Correlation.DefinitionDigest == "" || e.Correlation.Route == "" || e.Correlation.Model == "" {
		return ErrInvalid
	}
	if err := e.Correlation.Binding.Validate(); err != nil {
		return err
	}
	if err := e.Correlation.Environment.Validate(); err != nil {
		return err
	}
	if e.Correlation.Environment.OwnerID != e.Correlation.Binding.OwnerID {
		return ErrInvalid
	}
	bodies := 0
	for _, present := range []bool{e.Event != nil, e.Usage != nil, e.Delivery != nil, e.Execution != nil} {
		if present {
			bodies++
		}
	}
	if bodies != 1 {
		return ErrInvalid
	}
	switch e.Kind {
	case Event:
		if e.Event == nil {
			return ErrInvalid
		}
		actual, err := eventEntry(*e.Event)
		if err != nil {
			return err
		}
		if actual.Correlation != e.Correlation || !samePosition(actual.Position, e.Position) || e.Event.SessionID != string(e.Correlation.Binding.ConversationID) || e.Event.TurnID != string(e.Correlation.TurnID) {
			return ErrInvalid
		}
	case Usage:
		if e.Usage == nil {
			return ErrInvalid
		}
		if err := e.Usage.Validate(); err != nil {
			return err
		}
		if e.Usage.SessionID != string(e.Correlation.Binding.ConversationID) || e.Usage.MessageID != string(e.Correlation.ModelCallID) || e.Usage.Model != string(e.Correlation.Model) || strings.TrimSpace(e.Usage.Provider) == "" || e.Usage.RecordedAt.IsZero() {
			return ErrInvalid
		}
	case Delivery:
		if e.Delivery == nil {
			return ErrInvalid
		}
		if err := e.Delivery.Validate(); err != nil {
			return err
		}
		if e.Delivery.Binding != e.Correlation.Binding || e.Delivery.DecisionRef != string(e.Correlation.DecisionID) {
			return ErrInvalid
		}
	case Execution:
		if e.Execution == nil {
			return ErrInvalid
		}
		if err := e.Execution.Validate(); err != nil {
			return err
		}
		if e.Execution.Binding != e.Correlation.Binding || e.Execution.OperationID != string(e.Correlation.OperationID) || (e.Correlation.DecisionID != "" && e.Execution.DecisionRef != string(e.Correlation.DecisionID)) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if e.Kind != Event && !samePosition(e.Position, Position{}) {
		return ErrInvalid
	}
	return nil
}
func samePosition(a, b Position) bool {
	return a.SourceSequence == b.SourceSequence && a.ProducerGeneration == b.ProducerGeneration && a.CommittedCursor == b.CommittedCursor && ((a.PTYOffset == nil && b.PTYOffset == nil) || (a.PTYOffset != nil && b.PTYOffset != nil && *a.PTYOffset == *b.PTYOffset))
}
func copyValue[T any](v T) (out T, err error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}
