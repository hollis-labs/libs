package subprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/internal/strictjson"
)

const MethodGrantsRenew = "plugin/grants/renew"
const GrantsRenewalVersion = 1

// GrantsRenewParams is host initiated. It requires a request ID and an actual
// acknowledged response, never a notification. Sequence starts at one and is
// never reused on a connection, including after a failed attempt.
type GrantsRenewParams struct {
	RenewalVersion int                        `json:"renewal_version"`
	Sequence       uint64                     `json:"sequence"`
	Incarnation    capability.RuntimeIdentity `json:"incarnation"`
	Grants         capability.GrantSet        `json:"grants"`
	Context        ForwardContext             `json:"context"`
}
type GrantsRenewResult struct {
	RenewalVersion int                        `json:"renewal_version"`
	Sequence       uint64                     `json:"sequence"`
	Incarnation    capability.RuntimeIdentity `json:"incarnation"`
}

func (p *GrantsRenewParams) UnmarshalJSON(raw []byte) error {
	f, err := strictjson.Object(raw, "renewal_version", "sequence", "incarnation", "grants", "context")
	if err != nil {
		return fmt.Errorf("invalid grants renewal object")
	}
	for _, key := range []string{"renewal_version", "sequence"} {
		if !decimalToken(f[key]) {
			return fmt.Errorf("invalid grants renewal integer")
		}
	}
	type plain GrantsRenewParams
	var next plain
	if json.Unmarshal(raw, &next) != nil || next.RenewalVersion != GrantsRenewalVersion || next.Sequence == 0 || next.Sequence > capability.MaxSafeInteger || next.Grants.ValidateForRuntime(next.Incarnation) != nil || ValidateHostRPCDTO("ForwardContext", f["context"]) != nil || next.Context.BindingID != nil {
		return fmt.Errorf("invalid grants renewal fields")
	}
	*p = GrantsRenewParams(next)
	return nil
}
func decimalToken(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// GrantsRenewalHandler opts into acknowledged discovery updates. Implementations
// atomically replace their own cached grant metadata, respect ctx cancellation,
// and must not infer authority from it. Any failed/uncertain update fences the
// connection; hosts revoke and stop it rather than retrying a possibly applied update.
type GrantsRenewalHandler interface {
	GrantsRenewed(context.Context, capability.GrantSet) error
}

type grantsKey struct{}
type grantState struct {
	mu                                sync.RWMutex
	owner                             capability.RuntimeIdentity
	grants                            capability.GrantSet
	sequence                          uint64
	busy, ended, initialized, enabled bool
}

// GrantSetFromContext returns the current detached SDK discovery snapshot. It
// is not permission. Existing calls retain their original finite deadlines.
func GrantSetFromContext(ctx context.Context) (capability.GrantSet, bool) {
	if ctx == nil {
		return nil, false
	}
	state, ok := ctx.Value(grantsKey{}).(*grantState)
	if !ok || state == nil {
		return nil, false
	}
	return state.snapshot()
}
func (s *grantState) snapshot() (capability.GrantSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ended || !s.initialized {
		return nil, false
	}
	return s.grants.Clone(), true
}
func (s *grantState) end() { s.mu.Lock(); s.ended = true; s.mu.Unlock() }
func (s *server) renewGrants(ctx context.Context, req RPCRequest) {
	if !req.ID.positiveInteger() {
		s.writeError(req.ID, ErrCodeInvalidRequest, "grant renewal requires positive request ID", scopeFromContext(ctx))
		return
	}
	handler, ok := s.plugin.(GrantsRenewalHandler)
	if !ok {
		s.writeError(req.ID, ErrCodeMethodNotFound, "grant renewal not supported", scopeFromContext(ctx))
		return
	}
	var p GrantsRenewParams
	if err := decodeParams(req.Params, &p); err != nil {
		s.writeError(req.ID, ErrCodeInvalidParams, err.Error(), scopeFromContext(ctx))
		return
	}
	state := &s.grants
	state.mu.Lock()
	if !state.enabled || !state.initialized || state.ended || state.busy || p.Sequence != state.sequence+1 || p.Incarnation != state.owner || ctx.Err() != nil || state.grants.ValidateRenewal(p.Grants, state.owner, time.Now()) != nil {
		state.mu.Unlock()
		s.writeError(req.ID, ErrCodeInvalidParams, "grant renewal refused", scopeFromContext(ctx))
		return
	}
	state.busy = true
	state.sequence = p.Sequence
	received := time.Now()
	if scope := scopeFromContext(ctx); scope != nil {
		received = scope.arrivedAt
	}
	deadline := received.Add(time.Duration(p.Context.TimeoutMS) * time.Millisecond)
	for _, g := range state.grants {
		end, _ := time.Parse(time.RFC3339Nano, g.ExpiresAt)
		if end.Before(deadline) {
			deadline = end
		}
	}
	state.mu.Unlock()
	call, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	err := invokeGrantRenewal(handler, call, p.Grants.Clone())
	state.mu.Lock()
	if err == nil {
		err = call.Err()
	}
	if err == nil && (state.ended || state.grants.ValidateRenewal(p.Grants, state.owner, time.Now()) != nil) {
		err = fmt.Errorf("grant lease ended during renewal")
	}
	if err == nil {
		state.grants = p.Grants.Clone()
	} else {
		state.ended = true
	}
	state.busy = false
	state.mu.Unlock()
	if err != nil {
		s.writeErrorFromPluginErr(req.ID, err, scopeFromContext(ctx))
		s.fence(err)
		return
	}
	s.reverse.acceptedGrants(p.Grants)
	s.writeResult(req.ID, GrantsRenewResult{GrantsRenewalVersion, p.Sequence, p.Incarnation}, scopeFromContext(ctx))
}

func (r *GrantsRenewResult) UnmarshalJSON(raw []byte) error {
	f, err := strictjson.Object(raw, "renewal_version", "sequence", "incarnation")
	if err != nil || !decimalToken(f["renewal_version"]) || !decimalToken(f["sequence"]) {
		return fmt.Errorf("invalid grant renewal ack")
	}
	type plain GrantsRenewResult
	var next plain
	if json.Unmarshal(raw, &next) != nil || next.RenewalVersion != GrantsRenewalVersion || next.Sequence == 0 || next.Sequence > capability.MaxSafeInteger || next.Incarnation.Validate() != nil {
		return fmt.Errorf("invalid grant renewal ack")
	}
	*r = GrantsRenewResult(next)
	return nil
}

func invokeGrantRenewal(handler GrantsRenewalHandler, ctx context.Context, grants capability.GrantSet) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("grant renewal callback panicked")
		}
	}()
	return handler.GrantsRenewed(ctx, grants)
}

func (p GrantsRenewParams) Validate() error {
	if p.RenewalVersion != GrantsRenewalVersion || p.Sequence == 0 || p.Sequence > capability.MaxSafeInteger || p.Context.Validate() != nil || p.Context.BindingID != nil {
		return fmt.Errorf("invalid grants renewal fields")
	}
	return p.Grants.ValidateForRuntime(p.Incarnation)
}
func (p GrantsRenewParams) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type plain GrantsRenewParams
	return json.Marshal(plain(p))
}
func (r GrantsRenewResult) Validate() error {
	if r.RenewalVersion != GrantsRenewalVersion || r.Sequence == 0 || r.Sequence > capability.MaxSafeInteger {
		return fmt.Errorf("invalid grant renewal ack")
	}
	return r.Incarnation.Validate()
}
func (r GrantsRenewResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type plain GrantsRenewResult
	return json.Marshal(plain(r))
}
