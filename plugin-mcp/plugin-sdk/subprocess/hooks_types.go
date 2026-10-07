package subprocess

import (
	"context"
	"encoding/json"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

// HooksProfileVersion is acknowledged when Init offers it and the plugin implements HookHandler.
const HooksProfileVersion = 1
const MaxHookBatchItems = 64
const MaxHookDTOBytes = 1 << 20

// HookScope carries host-issued metadata, not authority. Hosts fence the live
// registration and binding; the SDK checks only structure and incarnation.
type HookScope struct {
	Incarnation    capability.RuntimeIdentity `json:"incarnation"`
	RegistrationID string                     `json:"registration_id"`
}
type HookTrace struct {
	TraceID      string  `json:"trace_id"`
	SpanID       string  `json:"span_id"`
	ParentSpanID *string `json:"parent_span_id,omitempty"`
}
type HookHandleParams struct {
	InvocationID       string            `json:"invocation_id"`
	CatalogVersion     string            `json:"catalog_version"`
	Hook               string            `json:"hook"`
	SchemaDigest       string            `json:"schema_digest"`
	Kind               string            `json:"kind"`
	Mode               string            `json:"mode"`
	Scope              HookScope         `json:"scope"`
	Context            ForwardContext    `json:"context"`
	Payload            json.RawMessage   `json:"payload"`
	Metadata           map[string]string `json:"metadata"`
	Deadline           string            `json:"deadline"`
	AggregateBudgetMS  uint32            `json:"aggregate_budget_ms"`
	Depth              uint32            `json:"depth"`
	Trace              HookTrace         `json:"trace"`
	RootInvocationID   string            `json:"root_invocation_id"`
	ParentInvocationID *string           `json:"parent_invocation_id,omitempty"`
}

// HookFailure is operational failure data, never an engine sentinel.
type HookFailure struct {
	Code    string  `json:"code"`
	Message *string `json:"message,omitempty"`
}

// HookHandleResult is flat on the wire. ValidateHookResultFor enforces its
// status branch and correlation with the originating request.
type HookHandleResult struct {
	InvocationID string          `json:"invocation_id"`
	Status       string          `json:"status"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Reason       *string         `json:"reason,omitempty"`
	Error        *HookFailure    `json:"error,omitempty"`
}
type HookHandleBatchParams struct {
	Items []HookHandleParams `json:"items"`
}
type HookHandleBatchResult struct {
	Items []HookHandleResult `json:"items"`
}

// HookHandler opts into hooks/1 when the host offers it during Init.
// Hook calls receive no host client; reverse callbacks are negotiated separately.
type HookHandler interface {
	HookHandle(context.Context, HookHandleParams) (HookHandleResult, error)
}

func (v HookScope) MarshalJSON() ([]byte, error) {
	type plain HookScope
	b, err := hookMarshal(plain(v))
	if err == nil {
		err = ValidateHookDTO("HookScope", b)
	}
	return b, err
}
func (v *HookScope) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookScope", b); err != nil {
		return err
	}
	type plain HookScope
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookScope", "invalid field type")
	}
	*v = HookScope(next)
	return nil
}
func (v HookScope) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HookTrace) MarshalJSON() ([]byte, error) {
	type plain HookTrace
	b, err := hookMarshal(plain(v))
	if err == nil {
		err = ValidateHookDTO("HookTrace", b)
	}
	return b, err
}
func (v *HookTrace) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookTrace", b); err != nil {
		return err
	}
	type plain HookTrace
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookTrace", "invalid field type")
	}
	*v = HookTrace(next)
	return nil
}
func (v HookTrace) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HookFailure) MarshalJSON() ([]byte, error) {
	type plain HookFailure
	b, err := hookMarshal(plain(v))
	if err == nil {
		err = ValidateHookDTO("HookFailure", b)
	}
	return b, err
}
func (v *HookFailure) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookFailure", b); err != nil {
		return err
	}
	type plain HookFailure
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookFailure", "invalid field type")
	}
	*v = HookFailure(next)
	return nil
}
func (v HookFailure) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HookHandleParams) MarshalJSON() ([]byte, error) {
	if len(v.Payload) == 0 {
		return nil, hookInvalid("payload", "required JSON payload")
	}
	type plain HookHandleParams
	b, err := hookPreservePayload(plain(v), v.Payload)
	if err == nil {
		err = ValidateHookDTO("HookHandleParams", b)
	}
	return b, err
}
func (v *HookHandleParams) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookHandleParams", b); err != nil {
		return err
	}
	type plain HookHandleParams
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookHandleParams", "invalid field type")
	}
	*v = HookHandleParams(next)
	return nil
}
func (v HookHandleParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HookHandleResult) MarshalJSON() ([]byte, error) {
	type plain HookHandleResult
	b, err := hookPreservePayload(plain(v), v.Payload)
	if err == nil {
		err = ValidateHookDTO("HookHandleResult", b)
	}
	return b, err
}
func (v *HookHandleResult) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookHandleResult", b); err != nil {
		return err
	}
	type plain HookHandleResult
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookHandleResult", "invalid field type")
	}
	*v = HookHandleResult(next)
	return nil
}
func (v HookHandleResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HookHandleBatchParams) MarshalJSON() ([]byte, error) {
	b, err := hookBatchJSON(v.Items)
	if err == nil {
		err = ValidateHookDTO("HookHandleBatchParams", b)
	}
	return b, err
}
func (v *HookHandleBatchParams) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookHandleBatchParams", b); err != nil {
		return err
	}
	type plain HookHandleBatchParams
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookHandleBatchParams", "invalid field type")
	}
	*v = HookHandleBatchParams(next)
	return nil
}
func (v HookHandleBatchParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HookHandleBatchResult) MarshalJSON() ([]byte, error) {
	b, err := hookBatchJSON(v.Items)
	if err == nil {
		err = ValidateHookDTO("HookHandleBatchResult", b)
	}
	return b, err
}
func (v *HookHandleBatchResult) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookHandleBatchResult", b); err != nil {
		return err
	}
	type plain HookHandleBatchResult
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookHandleBatchResult", "invalid field type")
	}
	*v = HookHandleBatchResult(next)
	return nil
}
func (v HookHandleBatchResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HookErrorData) MarshalJSON() ([]byte, error) {
	type plain HookErrorData
	b, err := hookMarshal(plain(v))
	if err == nil {
		err = ValidateHookDTO("HookErrorData", b)
	}
	return b, err
}
func (v *HookErrorData) UnmarshalJSON(b []byte) error {
	if err := ValidateHookDTO("HookErrorData", b); err != nil {
		return err
	}
	type plain HookErrorData
	var next plain
	if err := json.Unmarshal(b, &next); err != nil {
		return hookInvalid("HookErrorData", "invalid field type")
	}
	*v = HookErrorData(next)
	return nil
}
func (v HookErrorData) Validate() error { _, err := v.MarshalJSON(); return err }

// EncodeHookHandleParams validates and preserves opaque payload literals.
func EncodeHookHandleParams(v HookHandleParams) ([]byte, error) { return v.MarshalJSON() }
func DecodeHookHandleParams(raw []byte) (HookHandleParams, error) {
	var v HookHandleParams
	err := json.Unmarshal(raw, &v)
	return v, err
}

// EncodeHookHandleResult validates and preserves opaque payload literals.
func EncodeHookHandleResult(v HookHandleResult) ([]byte, error) { return v.MarshalJSON() }
func DecodeHookHandleResult(raw []byte) (HookHandleResult, error) {
	var v HookHandleResult
	err := json.Unmarshal(raw, &v)
	return v, err
}

// EncodeHookHandleBatchParams validates and preserves opaque payload literals.
func EncodeHookHandleBatchParams(v HookHandleBatchParams) ([]byte, error) { return v.MarshalJSON() }
func DecodeHookHandleBatchParams(raw []byte) (HookHandleBatchParams, error) {
	var v HookHandleBatchParams
	err := json.Unmarshal(raw, &v)
	return v, err
}

// EncodeHookHandleBatchResult validates and preserves opaque payload literals.
func EncodeHookHandleBatchResult(v HookHandleBatchResult) ([]byte, error) { return v.MarshalJSON() }
func DecodeHookHandleBatchResult(raw []byte) (HookHandleBatchResult, error) {
	var v HookHandleBatchResult
	err := json.Unmarshal(raw, &v)
	return v, err
}
