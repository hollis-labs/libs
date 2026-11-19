package gate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

func nonblank(s string) bool { return strings.TrimSpace(s) != "" }
func invalid(s string) error { return fmt.Errorf("%w: %s", ErrInvalid, s) }
func (id NativeRequestID) Validate() error {
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	var v any
	if !json.Valid(id) {
		return invalid("native request ID must be JSON string or number")
	}
	if err := d.Decode(&v); err != nil {
		return invalid("native request ID")
	}
	switch v.(type) {
	case string, json.Number:
		return nil
	default:
		return invalid("native request ID must be string or number")
	}
}
func (id NativeRequestID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return bytes.Clone(id), nil
}
func (id *NativeRequestID) UnmarshalJSON(b []byte) error {
	next := NativeRequestID(bytes.Clone(b))
	if err := next.Validate(); err != nil {
		return err
	}
	*id = next
	return nil
}
func (id NativeRequestID) Equal(other NativeRequestID) bool {
	return bytes.Equal(bytes.TrimSpace(id), bytes.TrimSpace(other))
}
func (a AdapterScope) Validate() error {
	if !nonblank(a.AdapterID) || !nonblank(a.Protocol) || !nonblank(a.LaunchMode) {
		return invalid("adapter, protocol and launch mode required")
	}
	return nil
}
func (k DecisionKind) valid() bool { return k == Allow || k == Deny || k == Cancel }
func (s Scope) Validate() error {
	if !nonblank(s.Target) || s.ExpiresAt.IsZero() {
		return invalid("scope target and expiry required")
	}
	switch s.Kind {
	case Once:
		if s.MaxUses != 1 {
			return invalid("once scope must have exactly one use")
		}
	case Bounded:
		if s.MaxUses == 0 {
			return invalid("bounded scope needs a positive limit")
		}
	case Session, PolicyAmendment:
	default:
		return fmt.Errorf("%w: scope %s", ErrUnsupported, s.Kind)
	}
	return nil
}
func sameScope(a, b Scope) bool {
	return a.Kind == b.Kind && a.Target == b.Target && a.MaxUses == b.MaxUses && a.ExpiresAt.Equal(b.ExpiresAt)
}
func (o NativeOption) Validate() error {
	if !nonblank(o.ID) || !o.Kind.valid() {
		return invalid("option ID and known decision kind required")
	}
	if err := o.Scope.Validate(); err != nil {
		return err
	}
	if len(o.Payload) > 0 && !json.Valid(o.Payload) {
		return invalid("invalid option payload JSON")
	}
	return nil
}
func (r Request) Validate() error {
	if r.ContractVersion != ContractVersion || !nonblank(r.IdempotencyKey) || len(r.IdempotencyKey) > hitl.MaxIdempotencyKey {
		return invalid("contract version or idempotency key")
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if err := r.Adapter.Validate(); err != nil {
		return err
	}
	if !nonblank(r.EnforcementPoint) || !nonblank(r.OperationID) || !nonblank(r.OperationClass) || r.ExpiresAt.IsZero() {
		return invalid("enforcement point, operation, class and expiry required")
	}
	if err := r.OperationDigest.Validate(); err != nil {
		return err
	}
	if !nonblank(r.OperationDigest.DescriptorVersion) {
		return invalid("operation descriptor version required")
	}
	if err := r.NativeRequestID.Validate(); err != nil {
		return err
	}
	if len(r.Options) == 0 {
		return invalid("native options required")
	}
	seen := map[string]bool{}
	for _, o := range r.Options {
		if err := o.Validate(); err != nil {
			return err
		}
		if seen[o.ID] {
			return invalid("duplicate option ID")
		}
		seen[o.ID] = true
		if o.Scope.ExpiresAt.After(r.ExpiresAt) {
			return invalid("option outlives request")
		}
	}
	if err := r.Policy.Validate(); err != nil {
		return err
	}
	for _, ref := range append(slices.Clone(r.ResourceVersions), r.Provenance...) {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	for _, o := range r.Obligations {
		if !nonblank(o.Kind) {
			return invalid("obligation kind required")
		}
		if err := o.Ref.Validate(); err != nil {
			return err
		}
	}
	if r.Environment != nil {
		if err := r.Environment.Validate(); err != nil {
			return err
		}
		if r.Environment.Receipt.OwnerID != r.Binding.OwnerID {
			return invalid("environment owner mismatch")
		}
	}
	return nil
}

// ValidateRequest rejects unsupported semantics before an adapter accepts an
// immutable request. The host still must authorize the actual offered options;
// provider advertising alone is not a permission ceiling.
func (c Capabilities) ValidateRequest(r Request, now time.Time) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return invalid("admission clock required")
	}
	if !now.Before(r.ExpiresAt) {
		return ErrExpired
	}
	if c.Adapter != r.Adapter || !slices.Contains(c.OperationClasses, r.OperationClass) {
		return ErrUnsupported
	}
	for _, o := range r.Options {
		if !slices.Contains(c.DecisionKinds, o.Kind) || !slices.Contains(c.ScopeKinds, o.Scope.Kind) {
			return ErrUnsupported
		}
		if !now.Before(o.Scope.ExpiresAt) {
			return ErrExpired
		}
	}
	for _, o := range r.Obligations {
		if !slices.Contains(c.ObligationKinds, o.Kind) {
			return fmt.Errorf("%w: mandatory obligation %s", ErrUnsupported, o.Kind)
		}
	}
	if r.Environment != nil {
		if err := r.Environment.ValidateAt(now, nil); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot returns a detached validated copy for a GatePort's acceptance path.
// Callers must not mutate the input concurrently with acceptance.
func (r Request) Snapshot() (Request, error) {
	if err := r.Validate(); err != nil {
		return Request{}, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return Request{}, err
	}
	var out Request
	err = json.Unmarshal(b, &out)
	return out, err
}

// Digest hashes the complete immutable request, including exact choices and
// opaque payloads. It is distinct from the host-recomputed operation digest.
// encoding/json serializes the known fields and compacts opaque JSON whitespace.
// Raw payload object-key order and numeric tokens remain significant: semantically
// equivalent reordered payloads can conservatively conflict. This is not a
// general canonical JSON algorithm. Native string/number identity is preserved.
func (r Request) Digest() (runtimecontract.Digest, error) {
	if err := r.Validate(); err != nil {
		return runtimecontract.Digest{}, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return runtimecontract.Digest{}, err
	}
	sum := sha256.Sum256(b)
	return runtimecontract.Digest{Scheme: "sha256", DescriptorVersion: "gate-request-v1", Value: hex.EncodeToString(sum[:])}, nil
}
func (t Ticket) Validate() error {
	if !nonblank(t.AuthorityRef) || t.Handle.ContractVersion != hitl.ContractVersion || !nonblank(t.Handle.ItemID) || t.Handle.Revision < 1 || !t.Handle.State.Valid() {
		return invalid("HITL authority and handle required")
	}
	if err := t.Binding.Validate(); err != nil {
		return err
	}
	if err := t.RequestDigest.Validate(); err != nil {
		return err
	}
	if t.RequestDigest.DescriptorVersion != "gate-request-v1" {
		return invalid("request digest version")
	}
	return nil
}

// ValidateAdmission refuses malformed, denied, expired or differently bound
// projections, then asks the trusted host to resolve authority. Success is only
// a precondition for the executor's atomic live grant claim, never permission to
// replay a tool or evidence that any external effect has happened.
func ValidateAdmission(ctx context.Context, r Request, d Decision, a Admission, v AuthorityVerifier) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if err := d.Ticket.Validate(); err != nil {
		return err
	}
	digest, err := r.Digest()
	if err != nil {
		return err
	}
	if d.Ticket.RequestDigest != digest || d.Ticket.Binding != r.Binding || a.Binding != r.Binding || a.Adapter != r.Adapter || a.EnforcementPoint != r.EnforcementPoint || a.OperationDigest != r.OperationDigest || d.OperationDigest != r.OperationDigest {
		return ErrBinding
	}
	if a.Now.IsZero() || d.ExpiresAt.IsZero() || d.Resolution.ResolvedAt.IsZero() {
		return invalid("decision and admission times required")
	}
	if !a.Now.Before(r.ExpiresAt) || !a.Now.Before(d.ExpiresAt) || d.ExpiresAt.After(r.ExpiresAt) || d.Resolution.ResolvedAt.After(a.Now) || !d.Resolution.ResolvedAt.Before(r.ExpiresAt) {
		return ErrExpired
	}
	if !nonblank(d.AuthorityRef) || !nonblank(d.Resolution.ResolutionID) || d.Resolution.InteractionRevision < d.Ticket.Handle.Revision {
		return invalid("authoritative resolution reference required")
	}
	if err := d.Resolution.Participant.Validate(); err != nil {
		return err
	}
	var selected *NativeOption
	for i := range r.Options {
		if r.Options[i].ID == d.OptionID {
			selected = &r.Options[i]
			break
		}
	}
	if selected == nil {
		return ErrUnsupported
	}
	if !sameScope(selected.Scope, d.Scope) {
		return ErrBinding
	}
	if !a.Now.Before(d.Scope.ExpiresAt) || d.ExpiresAt.After(d.Scope.ExpiresAt) {
		return ErrExpired
	}
	if selected.Kind != Allow {
		return ErrDenied
	}
	if r.Environment != nil {
		if err := r.Environment.ValidateAt(a.Now, nil); err != nil {
			return err
		}
	}
	if v == nil {
		return ErrAuthority
	}
	if err := v.Verify(ctx, r, d); err != nil {
		return fmt.Errorf("%w: %w", ErrAuthority, err)
	}
	return ctx.Err()
}

func (r DeliveryReceipt) Validate() error {
	if !nonblank(r.DecisionRef) || !nonblank(r.AdapterID) {
		return invalid("decision and adapter required")
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if err := r.NativeRequestID.Validate(); err != nil {
		return err
	}
	switch r.Status {
	case DeliveryPending, DeliverySent, DeliveryAcknowledged, DeliveryRejected, DeliveryUnknown:
		return nil
	}
	return invalid("delivery status")
}
func (r ExecutionReceipt) Validate() error {
	if !nonblank(r.DecisionRef) || !nonblank(r.OperationID) {
		return invalid("decision and operation required")
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	switch r.State {
	case ExecutionNotStarted, ExecutionStarted, ExecutionSucceeded, ExecutionFailed, ExecutionCancelled, ExecutionUnknown:
		return nil
	}
	return invalid("execution state")
}
func (w WaitOptions) Validate() error {
	if w.MaxWait < 0 || w.MaxWait > time.Duration(hitl.MaxWaitMs)*time.Millisecond {
		return invalid("wait exceeds HITL bounds")
	}
	return nil
}
