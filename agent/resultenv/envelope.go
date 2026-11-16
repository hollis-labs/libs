package resultenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

const Version = "resultenv/v1"

type Outcome string

const (
	Success        Outcome = "success"
	NonzeroExit    Outcome = "nonzero_exit"
	Signal         Outcome = "signal"
	ToolError      Outcome = "tool_error"
	TransportError Outcome = "transport_error"
	Timeout        Outcome = "timeout"
	Cancelled      Outcome = "cancelled"
	Unknown        Outcome = "unknown"
	NotRun         Outcome = "not_run"
)

var ErrInvalid = errors.New("resultenv: invalid envelope")

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Truncation struct {
	Truncated bool   `json:"truncated"`
	Cursor    string `json:"cursor,omitempty"`
}

// Envelope keeps the entire MCP result as JSON, including content kinds,
// structuredContent, annotations and _meta. Redact contains JSON pointers to
// fields that the derived model view must mask; it never edits MCPResult.
type Envelope struct {
	Version         string          `json:"version"`
	Outcome         Outcome         `json:"outcome"`
	MCPResult       json.RawMessage `json:"mcp_result,omitempty"`
	OutputSchemaRef string          `json:"output_schema_ref,omitempty"`
	Truncation      *Truncation     `json:"truncation,omitempty"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	Signal          string          `json:"signal,omitempty"`
	CancelRequested *bool           `json:"cancel_requested,omitempty"`
	CancelConfirmed *bool           `json:"cancel_confirmed,omitempty"`
	Stdout          string          `json:"stdout,omitempty"`
	Stderr          string          `json:"stderr,omitempty"`
	CombinedOutput  string          `json:"combined_output,omitempty"`
	Error           *Failure        `json:"error,omitempty"`
	Redact          []string        `json:"redact,omitempty"`
}

func (e Envelope) Validate() error {
	if e.Version != Version {
		return fmt.Errorf("%w: version", ErrInvalid)
	}
	switch e.Outcome {
	case Success, NonzeroExit, Signal, ToolError, TransportError, Timeout, Cancelled, Unknown, NotRun:
	default:
		return fmt.Errorf("%w: outcome", ErrInvalid)
	}
	if len(e.MCPResult) != 0 {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(e.MCPResult, &value); err != nil || value == nil {
			return fmt.Errorf("%w: MCP result must be an object", ErrInvalid)
		}
	}
	if e.Outcome == Success && len(e.MCPResult) == 0 && (e.ExitCode == nil || *e.ExitCode != 0) {
		return fmt.Errorf("%w: success requires MCP result or zero exit code", ErrInvalid)
	}
	if e.Outcome == ToolError && len(e.MCPResult) == 0 && e.Error == nil {
		return fmt.Errorf("%w: tool error requires MCP result or error", ErrInvalid)
	}
	if e.Outcome == NonzeroExit && (e.ExitCode == nil || *e.ExitCode == 0) {
		return fmt.Errorf("%w: nonzero exit code required", ErrInvalid)
	}
	if e.Outcome == Signal && strings.TrimSpace(e.Signal) == "" {
		return fmt.Errorf("%w: signal name required", ErrInvalid)
	}
	if e.Error != nil && strings.TrimSpace(e.Error.Code) == "" {
		return fmt.Errorf("%w: error code required", ErrInvalid)
	}
	for _, pointer := range e.Redact {
		if !strings.HasPrefix(pointer, "/") {
			return fmt.Errorf("%w: redaction must be a JSON pointer", ErrInvalid)
		}
	}
	return nil
}

// FromMCPRaw preserves the exact raw MCP result bytes. Callers should capture
// the protocol payload before projecting it into a text-only SDK ToolResult.
// It performs no retry and makes no claim that an absent result did not run.
func FromMCPRaw(raw []byte, outputSchemaRef string) (Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Envelope{}, fmt.Errorf("%w: MCP result must be an object", ErrInvalid)
	}
	if _, ok := fields["content"]; !ok {
		return Envelope{}, fmt.Errorf("%w: MCP content required", ErrInvalid)
	}
	var isError bool
	if value, ok := fields["isError"]; ok {
		if err := json.Unmarshal(value, &isError); err != nil {
			return Envelope{}, fmt.Errorf("%w: isError must be boolean", ErrInvalid)
		}
	}
	outcome := Success
	if isError {
		outcome = ToolError
	}
	e := Envelope{Version: Version, Outcome: outcome, MCPResult: append(json.RawMessage(nil), raw...), OutputSchemaRef: outputSchemaRef}
	return e, e.Validate()
}

// Receipt is the host's lossless record. Ref is a host-issued immutable
// locator, not a permission grant. EnvelopeJSON is retained byte for byte.
// Its []byte JSON representation is base64, so receipt serialization cannot
// reformat the bytes covered by the digest.
type Receipt struct {
	Ref          runtimecontract.Reference `json:"ref"`
	EnvelopeJSON []byte                    `json:"envelope_bytes"`
}

func NewReceipt(ref runtimecontract.Reference, envelopeJSON []byte) (Receipt, error) {
	if err := ref.Validate(); err != nil {
		return Receipt{}, err
	}
	var e Envelope
	if err := json.Unmarshal(envelopeJSON, &e); err != nil {
		return Receipt{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := e.Validate(); err != nil {
		return Receipt{}, err
	}
	sum := sha256.Sum256(envelopeJSON)
	if !strings.EqualFold(ref.Digest.Value, hex.EncodeToString(sum[:])) {
		return Receipt{}, fmt.Errorf("%w: receipt digest mismatch", ErrInvalid)
	}
	return Receipt{Ref: ref, EnvelopeJSON: bytes.Clone(envelopeJSON)}, nil
}

func (r Receipt) Envelope() ([]byte, error) {
	if _, err := NewReceipt(r.Ref, r.EnvelopeJSON); err != nil {
		return nil, err
	}
	return bytes.Clone(r.EnvelopeJSON), nil
}

// AutoRetryAllowed is deliberately false for every outcome. A caller may make
// a new operation after reconciliation, but this contract never repeats an
// external effect. In particular, Unknown is distinct from NotRun.
func (e Envelope) AutoRetryAllowed() bool { return false }
