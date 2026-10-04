package subprocess

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
)

// HostClient is an SDK-owned request-scoped client. Authors obtain it from
// HostClientFromContext, never implement it or construct it. Later service
// methods may be added to this same type. Production activation is separate.
type HostClient struct {
	scope    *requestScope
	core     *correlation
	grants   map[string]capability.Grant
	ceilings map[string]uint32
	secrets  *secretTracker
}
type hostClientKey struct{}

// HostClientFromContext is unavailable in base and hooks-only connections.
// A cached client never grants a new request or connection any authority.
func HostClientFromContext(ctx context.Context) (*HostClient, bool) {
	if ctx == nil {
		return nil, false
	}
	h, ok := ctx.Value(hostClientKey{}).(*HostClient)
	return h, ok && h != nil && h.scope == scopeFromContext(ctx) && h.scope.acceptsResult()
}

// Internal activation only, pending negotiated reverse lifecycle integration.
// No guessed binding expiry: zero means its live lease is enforced by the host.
func hostClientContext(ctx context.Context, core *correlation, p InitParams, secrets *secretTracker, bindingExpiry time.Time) (context.Context, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	scope := scopeFromContext(ctx)
	if scope == nil || core == nil || !core.directional || p.HostServices == nil || scope.binding == nil || secrets == nil {
		return nil, localHostFailure(capability.TargetUnavailable)
	}
	scope.initLease(bindingExpiry)
	h := &HostClient{scope: scope, core: core, secrets: secrets, grants: map[string]capability.Grant{}, ceilings: map[string]uint32{}}
	for _, g := range p.Grants {
		g.Scope = append(json.RawMessage(nil), g.Scope...)
		h.grants[g.GrantID] = g
	}
	for method, ms := range p.HostServices.Limits.MethodTimeoutMS {
		h.ceilings[method] = ms
	}
	return context.WithValue(ctx, hostClientKey{}, h), nil
}
func localHostFailure(code capability.Code) error {
	return &capability.Error{Code: code, EffectState: capability.NotStarted}
}

// ReverseContext is built from the local scope, never accepted from the author.
func (h *HostClient) begin(ctx context.Context, method, grantID, descriptor string) (context.Context, context.CancelFunc, ReverseContext, error) {
	started := time.Now()
	fail := func(err error) (context.Context, context.CancelFunc, ReverseContext, error) {
		return nil, nil, ReverseContext{}, err
	}
	if h == nil || h.core == nil || h.scope == nil || ctx == nil || scopeFromContext(ctx) != h.scope || !h.scope.acceptsResult() {
		return fail(localHostFailure(capability.TargetUnavailable))
	}
	if h.scope.method == MethodHookHandle || h.scope.method == MethodHookHandleBatch {
		return fail(localHostFailure(capability.TargetUnavailable))
	}
	if lifecycleMethod(h.scope.method) && method != "host/log" {
		return fail(localHostFailure(capability.TargetUnavailable))
	}
	ceiling := h.ceilings[method]
	if ceiling == 0 {
		return fail(localHostFailure(capability.UnsupportedCapability))
	}
	g, ok := h.grants[grantID]
	if !ok || method != "host/bindings/renew" && (g.Name != descriptor || g.SchemaVersion != 1) {
		return fail(localHostFailure(capability.CapabilityDenied))
	}
	id, valid := h.scope.id.Integer()
	if !valid || id <= 0 || h.scope.binding == nil {
		return fail(localHostFailure(capability.TargetUnavailable))
	}
	end := started.Add(time.Duration(ceiling) * time.Millisecond)
	// Convert this validated snapshot expiry to a local monotonic remainder.
	expiry, err := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if err != nil {
		return fail(err)
	}
	expiry = started.Add(expiry.Sub(started))
	if expiry.Before(end) {
		end = expiry
	}
	if leaseEnd := h.scope.leaseDeadline(); !leaseEnd.IsZero() && leaseEnd.Before(end) {
		end = leaseEnd
	}
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(end) {
		end = deadline
	}
	if deadline, ok := h.scope.ctx.Deadline(); ok && deadline.Before(end) {
		end = deadline
	}
	callCtx, cancel := context.WithDeadlineCause(ctx, end, &DeadlineExceededError{})
	if cause := requestContextFailure(callCtx); cause != nil {
		cancel()
		return fail(cause)
	}
	remaining := time.Until(end) / time.Millisecond
	if remaining < 1 {
		cancel()
		return fail(&DeadlineExceededError{})
	}
	return callCtx, cancel, ReverseContext{BindingID: *h.scope.binding, TimeoutMS: uint32(remaining), ParentCall: ParentCall{RequestOwner: HostRPCOwnerHost, ID: uint64(id)}}, nil
}
func (h *HostClient) invoke(ctx context.Context, method, grantID, descriptor string, params func(ReverseContext) any, output any) error {
	callCtx, cancel, reverse, err := h.begin(ctx, method, grantID, descriptor)
	if err != nil {
		return err
	}
	defer cancel()
	release := h.scope.retain()
	defer release()
	raw, err := marshalBounded(params(reverse), MaxHostRPCDTOBytes)
	if err != nil {
		return err
	}
	if !h.scope.acceptsResult() {
		return localHostFailure(capability.TargetUnavailable)
	}
	done, err := h.core.callContext(callCtx, method, raw)
	if err != nil {
		return err
	}
	result := <-done
	if result.err != nil {
		var fault *RPCError
		if errors.As(result.err, &fault) && fault.Code == capability.HostRPCErrorCode {
			var data HostRPCErrorData
			encoded, e := marshalBounded(fault.Data, MaxHostRPCDTOBytes)
			if e != nil {
				return e
			}
			if err := json.Unmarshal(encoded, &data); err != nil {
				return err
			}
			return &HostRPCError{Code: fault.Code, Message: fault.Message, Data: data}
		}
		return result.err
	}
	return json.Unmarshal(result.result, output)
}

// Helper arguments contain only explicit grant selection and business input.
// Existing Params DTOs remain the single wire contract.
type StorageGetArgs struct{ GrantID, Key string }
type StoragePutArgs struct {
	GrantID, Key     string
	Value            json.RawMessage
	ExpectedRevision *string
	OperationKey     string
}
type StorageDeleteArgs struct{ GrantID, Key, ExpectedRevision, OperationKey string }
type SecretsGetArgs struct{ GrantID, SecretRef string }
type EgressRequestArgs struct {
	GrantID, Method, URL     string
	Headers                  *[]HostRPCHeader
	BodyBase64, OperationKey *string
}
type EventsPublishArgs struct {
	GrantID, EventName string
	Payload            json.RawMessage
	OperationKey       string
}
type HostLogArgs struct {
	GrantID, Level, Message string
	Fields                  *[]HostRPCLogField
}

// SecretValue is an owned byte copy, registered for SDK redaction before return.
// ExpiresAt is host metadata, not a local cache renewal or permission grant.
type SecretValue struct {
	Value     []byte
	ExpiresAt string
}

func (h *HostClient) StorageGet(ctx context.Context, a StorageGetArgs) (r StorageGetResult, err error) {
	err = h.invoke(ctx, "host/storage/get", a.GrantID, capability.StorageRead, func(c ReverseContext) any { return StorageGetParams{GrantID: a.GrantID, Context: c, Key: a.Key} }, &r)
	return
}
func (h *HostClient) StoragePut(ctx context.Context, a StoragePutArgs) (r StoragePutResult, err error) {
	err = h.invoke(ctx, "host/storage/put", a.GrantID, capability.StorageWrite, func(c ReverseContext) any {
		return StoragePutParams{GrantID: a.GrantID, Context: c, Key: a.Key, Value: a.Value, ExpectedRevision: a.ExpectedRevision, OperationKey: a.OperationKey}
	}, &r)
	if err == nil && r.OperationKey != a.OperationKey {
		err = h.badReceipt()
		r = StoragePutResult{}
	}
	return
}
func (h *HostClient) StorageDelete(ctx context.Context, a StorageDeleteArgs) (r StorageDeleteResult, err error) {
	err = h.invoke(ctx, "host/storage/delete", a.GrantID, capability.StorageWrite, func(c ReverseContext) any {
		return StorageDeleteParams{GrantID: a.GrantID, Context: c, Key: a.Key, ExpectedRevision: a.ExpectedRevision, OperationKey: a.OperationKey}
	}, &r)
	if err == nil && r.OperationKey != a.OperationKey {
		err = h.badReceipt()
		r = StorageDeleteResult{}
	}
	return
}
func (h *HostClient) SecretsGet(ctx context.Context, a SecretsGetArgs) (SecretValue, error) {
	var r SecretsGetResult
	err := h.invoke(ctx, "host/secrets/get", a.GrantID, capability.SecretsRead, func(c ReverseContext) any {
		return SecretsGetParams{GrantID: a.GrantID, Context: c, SecretRef: a.SecretRef}
	}, &r)
	if err != nil {
		return SecretValue{}, err
	}
	// DTO validation bounded the base64 text before this allocation.
	value, err := base64.StdEncoding.DecodeString(r.ValueBase64)
	if err != nil {
		return SecretValue{}, err
	}
	if h.secrets != nil {
		h.secrets.add(r.ValueBase64)
		h.secrets.add(string(value))
	}
	return SecretValue{Value: value, ExpiresAt: r.ExpiresAt}, nil
}
func (h *HostClient) EgressRequest(ctx context.Context, a EgressRequestArgs) (r EgressRequestResult, err error) {
	var operationKey *string
	err = h.invoke(ctx, "host/egress/request", a.GrantID, capability.EgressRequest, func(c ReverseContext) any {
		if a.OperationKey != nil {
			key := *a.OperationKey
			operationKey = &key
		}
		return EgressRequestParams{GrantID: a.GrantID, Context: c, Method: a.Method, URL: a.URL, Headers: a.Headers, BodyBase64: a.BodyBase64, OperationKey: operationKey}
	}, &r)
	if err == nil && ((operationKey == nil) != (r.OperationKey == nil) || operationKey != nil && *operationKey != *r.OperationKey) {
		err = h.badReceipt()
		r = EgressRequestResult{}
	}
	return
}
func (h *HostClient) EventsPublish(ctx context.Context, a EventsPublishArgs) (r EventsPublishResult, err error) {
	err = h.invoke(ctx, "host/events/publish", a.GrantID, capability.EventsPublish, func(c ReverseContext) any {
		return EventsPublishParams{GrantID: a.GrantID, Context: c, EventName: a.EventName, Payload: a.Payload, OperationKey: a.OperationKey}
	}, &r)
	if err == nil && r.OperationKey != a.OperationKey {
		err = h.badReceipt()
		r = EventsPublishResult{}
	}
	return
}
func (h *HostClient) badReceipt() error {
	h.core.close(errCorrelation)
	return &capability.Error{Code: capability.UnknownOutcome, EffectState: capability.Unknown}
}

func (h *HostClient) Log(ctx context.Context, a HostLogArgs) (r LogResult, err error) {
	err = h.invoke(ctx, "host/log", a.GrantID, capability.LogWrite, func(c ReverseContext) any {
		original := LogParams{GrantID: a.GrantID, Context: c, Level: a.Level, Message: a.Message, Fields: a.Fields}
		raw, e := marshalBounded(original, MaxHostRPCDTOBytes)
		if e != nil || ValidateHostRPCDTO("LogParams", raw) != nil {
			return original
		}
		redact, maskAll := h.secrets.redactor()
		var fields *[]HostRPCLogField
		if a.Fields != nil {
			items := make([]HostRPCLogField, len(*a.Fields))
			for i, f := range *a.Fields {
				raw := f.Value
				if maskAll {
					raw = json.RawMessage(`"[REDACTED]"`)
				} else {
					var e error
					raw, e = redactLogJSON(raw, redact)
					if e != nil {
						raw = json.RawMessage(`"[REDACTED]"`)
					}
				}
				items[i] = HostRPCLogField{Name: redact(f.Name), Value: raw}
			}
			fields = &items
		}
		return LogParams{GrantID: a.GrantID, Context: c, Level: a.Level, Message: redact(a.Message), Fields: fields}
	}, &r)
	return
}
