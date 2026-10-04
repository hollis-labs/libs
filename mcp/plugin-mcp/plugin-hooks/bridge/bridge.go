// Package bridge is a never-tagged reference mapping for host adapters.
// Hosts copy or reimplement it; it is not a production dependency.
package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
	"github.com/hollis-labs/plugin-sdk/capability"
	sdk "github.com/hollis-labs/plugin-sdk/subprocess"
)

// Incarnation is supplied by the host's registration ledger, not derived from
// opaque hooks generation strings. Wire identity is diagnostic, not authority.
func Request(q hooks.RemoteRequest, incarnation capability.RuntimeIdentity) (sdk.HookHandleParams, error) {
	if incarnation.HostInstance != q.Scope.HostInstance || incarnation.OwnerID != q.Scope.Owner {
		return sdk.HookHandleParams{}, hooks.ErrInvalidOptions
	}
	timeout, err := milliseconds(q.Context.Timeout)
	if err != nil {
		return sdk.HookHandleParams{}, err
	}
	aggregate, err := milliseconds(q.Context.AggregateBudget)
	if err != nil {
		return sdk.HookHandleParams{}, err
	}
	if q.Context.Depth < 1 || uint64(q.Context.Depth) > math.MaxUint32 {
		return sdk.HookHandleParams{}, hooks.ErrInvalidOptions
	}
	p := sdk.HookHandleParams{InvocationID: q.InvocationID, CatalogVersion: q.CatalogVersion, Hook: q.Hook, SchemaDigest: q.SchemaDigest, Kind: string(q.Kind), Mode: string(q.Mode), Scope: sdk.HookScope{Incarnation: incarnation, RegistrationID: q.Scope.RegistrationID}, Context: sdk.ForwardContext{TimeoutMS: timeout}, Payload: bytes.Clone(q.Payload), Metadata: cloneMetadata(q.Metadata), Deadline: q.Context.Deadline.UTC().Format(time.RFC3339Nano), AggregateBudgetMS: aggregate, Depth: uint32(q.Context.Depth), Trace: sdk.HookTrace{TraceID: q.Context.Trace.TraceID, SpanID: q.Context.Trace.SpanID}, RootInvocationID: q.Context.RootInvocationID}
	if q.Context.BindingID != "" {
		p.Context.BindingID = pointer(sdk.BindingID(q.Context.BindingID))
	}
	if q.Context.ParentInvocationID != "" {
		p.ParentInvocationID = pointer(q.Context.ParentInvocationID)
	}
	if err := sdk.ValidateHookRequest(p, q.Context.BindingID == ""); err != nil {
		return sdk.HookHandleParams{}, err
	}
	return p, nil
}

// RestoreRequest validates wire fields against the original host ledger
// snapshot. Binding, generation, monotonic deadlines, trace and ancestry always
// come from that snapshot; plugin-supplied diagnostics cannot replace them.
func RestoreRequest(p sdk.HookHandleParams, host hooks.RemoteRequest, incarnation capability.RuntimeIdentity) (hooks.RemoteRequest, error) {
	expected, err := Request(host, incarnation)
	if err != nil {
		return hooks.RemoteRequest{}, err
	}
	got, err := sdk.EncodeHookHandleParams(p)
	if err != nil {
		return hooks.RemoteRequest{}, err
	}
	want, err := sdk.EncodeHookHandleParams(expected)
	if err != nil {
		return hooks.RemoteRequest{}, err
	}
	// Canonicalize structural field order only. Payloads are compared literally.
	var a, b map[string]json.RawMessage
	if json.Unmarshal(got, &a) != nil || json.Unmarshal(want, &b) != nil {
		return hooks.RemoteRequest{}, hooks.ErrInvalidOutput
	}
	payloadA, payloadB := a["payload"], b["payload"]
	delete(a, "payload")
	delete(b, "payload")
	var structuralA, structuralB map[string]any
	ca, _ := json.Marshal(a)
	cb, _ := json.Marshal(b)
	decoderA := json.NewDecoder(bytes.NewReader(ca))
	decoderA.UseNumber()
	decoderB := json.NewDecoder(bytes.NewReader(cb))
	decoderB.UseNumber()
	if decoderA.Decode(&structuralA) != nil || decoderB.Decode(&structuralB) != nil || !reflect.DeepEqual(structuralA, structuralB) || !bytes.Equal(payloadA, payloadB) {
		return hooks.RemoteRequest{}, hooks.ErrInvalidOutput
	}
	host.Payload = bytes.Clone(p.Payload)
	host.Metadata = cloneMetadata(p.Metadata)
	return host, nil
}

// Result decodes with SDK codecs and validates correlation and legal status
// branches BEFORE mapping. No RPC error code or diagnostic text becomes a veto.
// The hooks engine reconstructs its deliberate veto sentinels from this status.
func Result(p sdk.HookHandleParams, raw []byte) (hooks.RemoteResult, error) {
	r, err := sdk.DecodeHookHandleResult(raw)
	if err != nil {
		return invalid(p.InvocationID), nil
	}
	if err = sdk.ValidateHookResultFor(p, r); err != nil {
		return invalid(p.InvocationID), nil
	}
	out := hooks.RemoteResult{InvocationID: r.InvocationID, Status: hooks.RemoteStatus(r.Status), Payload: bytes.Clone(r.Payload)}
	if r.Reason != nil {
		out.Reason = *r.Reason
	}
	if r.Error != nil {
		failure, err := Failure(*r.Error)
		if err != nil {
			return invalid(p.InvocationID), nil
		}
		out.Failure = &failure
	}
	return out, nil
}
func WireResult(p sdk.HookHandleParams, r hooks.RemoteResult) ([]byte, error) {
	out := sdk.HookHandleResult{InvocationID: r.InvocationID, Status: string(r.Status), Payload: bytes.Clone(r.Payload)}
	if r.Reason != "" {
		out.Reason = pointer(r.Reason)
	}
	if r.Failure != nil {
		f, err := WireFailure(*r.Failure)
		if err != nil {
			return nil, err
		}
		out.Error = &f
	}
	if err := sdk.ValidateHookResultFor(p, out); err != nil {
		return nil, err
	}
	return sdk.EncodeHookHandleResult(out)
}
func Failure(f sdk.HookFailure) (hooks.RemoteFailure, error) {
	if err := f.Validate(); err != nil {
		return hooks.RemoteFailure{}, err
	}
	out := hooks.RemoteFailure{Code: hooks.RemoteFailureCode(f.Code)}
	if f.Message != nil {
		out.Message = *f.Message
	}
	return out, nil
}
func WireFailure(f hooks.RemoteFailure) (sdk.HookFailure, error) {
	out := sdk.HookFailure{Code: string(f.Code)}
	if f.Message != "" {
		out.Message = pointer(f.Message)
	}
	return out, out.Validate()
}
func BatchRequest(requests []hooks.RemoteRequest, identities []capability.RuntimeIdentity) (sdk.HookHandleBatchParams, error) {
	if len(requests) != len(identities) {
		return sdk.HookHandleBatchParams{}, hooks.ErrInvalidOptions
	}
	out := sdk.HookHandleBatchParams{Items: make([]sdk.HookHandleParams, len(requests))}
	for i, q := range requests {
		p, err := Request(q, identities[i])
		if err != nil {
			return sdk.HookHandleBatchParams{}, err
		}
		out.Items[i] = p
	}
	if err := sdk.ValidateHookBatchRequest(out, false); err != nil {
		return sdk.HookHandleBatchParams{}, err
	}
	return out, nil
}
func BatchResult(p sdk.HookHandleBatchParams, raw []byte) ([]hooks.RemoteResult, error) {
	r, err := sdk.DecodeHookHandleBatchResult(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid batch", hooks.ErrInvalidOutput)
	}
	if err = sdk.ValidateHookBatchResultFor(p, r); err != nil {
		return nil, fmt.Errorf("%w: uncorrelated batch", hooks.ErrInvalidOutput)
	}
	out := make([]hooks.RemoteResult, len(r.Items))
	for i, item := range r.Items {
		b, err := sdk.EncodeHookHandleResult(item)
		if err != nil {
			return nil, err
		}
		out[i], err = Result(p.Items[i], b)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func WireBatchResult(p sdk.HookHandleBatchParams, results []hooks.RemoteResult) ([]byte, error) {
	if len(p.Items) != len(results) {
		return nil, hooks.ErrInvalidOutput
	}
	out := sdk.HookHandleBatchResult{Items: make([]sdk.HookHandleResult, len(results))}
	for i, r := range results {
		raw, err := WireResult(p.Items[i], r)
		if err != nil {
			return nil, err
		}
		out.Items[i], err = sdk.DecodeHookHandleResult(raw)
		if err != nil {
			return nil, err
		}
	}
	return sdk.EncodeHookHandleBatchResult(out)
}
func Notification(n hooks.RemoteNotification, incarnation capability.RuntimeIdentity) (sdk.HookHandleParams, error) {
	if n.Request.Context.BindingID != "" || n.Request.Binding.ID() != "" {
		return sdk.HookHandleParams{}, hooks.ErrInvalidOptions
	}
	p, err := Request(n.Request, incarnation)
	if err != nil {
		return sdk.HookHandleParams{}, err
	}
	return p, sdk.ValidateHookRequest(p, true)
}
func invalid(id string) hooks.RemoteResult {
	return hooks.RemoteResult{InvocationID: id, Status: hooks.RemoteFailed, Failure: &hooks.RemoteFailure{Code: hooks.InvalidOutput}}
}
func milliseconds(d time.Duration) (uint32, error) {
	// Round down, never grant a longer receiver lease than the host snapshot.
	n := d / time.Millisecond
	if n < 1 || uint64(n) > math.MaxUint32 {
		return 0, hooks.ErrInvalidOptions
	}
	return uint32(n), nil
}
func cloneMetadata(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func pointer[T any](v T) *T { return &v }
