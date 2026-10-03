package host

import (
	"context"
	"encoding/json"

	"github.com/hollis-labs/plugin-sdk/capability"
)

// Request is a host-normalized planning input; it is not an alternative Init
// wire DTO. Each request resolves independently, including repeated names with
// distinct operation/target pairs. The host supplies reviewed policy inputs.
type Request struct {
	Name          string
	SchemaVersion uint32
	Reason        string
	Optional      bool
	Scope         capability.Scope
}
type ScopeInputs struct{ Supported, Approved, Policy capability.Scope }
type ScopeResolver interface {
	Scopes(context.Context, Request) (ScopeInputs, error)
}
type Denial struct {
	Capability string
	Code       capability.Code
}

// GrantPlan supplies host-issued identity/time/revision metadata and fresh grant
// IDs. It does not allocate incarnations or authenticate the requesting plugin.
type GrantPlan struct {
	Runtime                                       capability.RuntimeIdentity
	Audience, IssuedAt, ExpiresAt, PolicyRevision string
	NewGrantID                                    func() string
}

// ResolveGrants intersects requested, supported, approved and policy authority.
// Optional refusals produce named denials; any required refusal aborts the whole
// plan. No handler/credential/registration is installed. Hosts call it before
// spawn and activation, then serialize the shared Grant DTO into the sole Init.
func ResolveGrants(ctx context.Context, catalog *capability.Catalog, resolver ScopeResolver, requests []Request, plan GrantPlan) (capability.GrantSet, []Denial, error) {
	if err := contextFailure(ctx, ""); err != nil {
		return nil, nil, safeFailure(err, "", "")
	}
	if catalog == nil || resolver == nil || plan.NewGrantID == nil || plan.Runtime.Validate() != nil {
		return nil, nil, refusal(capability.InvalidRequest, "")
	}
	grants := capability.GrantSet{}
	denials := []Denial{}
	ids := map[string]bool{}
	for _, request := range requests {
		grant, err := resolveGrant(ctx, catalog, resolver, request, plan)
		if err != nil {
			failure := safeFailure(err, request.Name, "")
			// Cancellation and implementation failures must not be hidden as optional
			// feature denial. The host cannot proceed with an incomplete plan.
			if !request.Optional || failure.Code == capability.Cancelled || failure.Code == capability.DeadlineExceeded || failure.Code == capability.InternalError {
				return nil, denials, failure
			}
			denials = append(denials, Denial{request.Name, failure.Code})
			continue
		}
		if ids[grant.GrantID] {
			return nil, denials, refusal(capability.InvalidRequest, request.Name)
		}
		ids[grant.GrantID] = true
		grants = append(grants, grant)
	}
	// Validate the metadata even for an empty plan; zero grants convey no authority.
	probe := capability.Grant{GrantID: "validation", Name: capability.ReadonlyQuery, SchemaVersion: 1, Scope: json.RawMessage(`{}`), HostInstance: plan.Runtime.HostInstance, OwnerID: plan.Runtime.OwnerID, OwnerGeneration: plan.Runtime.OwnerGeneration, Audience: plan.Audience, IssuedAt: plan.IssuedAt, ExpiresAt: plan.ExpiresAt, PolicyRevision: plan.PolicyRevision}
	if probe.Validate() != nil {
		return nil, denials, refusal(capability.InvalidRequest, "")
	}
	return grants, denials, nil
}
func resolveGrant(ctx context.Context, catalog *capability.Catalog, resolver ScopeResolver, request Request, plan GrantPlan) (capability.Grant, error) {
	if err := contextFailure(ctx, request.Name); err != nil {
		return capability.Grant{}, err
	}
	if uint64(request.SchemaVersion) > uint64(^uint(0)>>1) {
		return capability.Grant{}, refusal(capability.UnsupportedCapability, request.Name)
	}
	descriptor, err := catalog.Lookup(request.Name, int(request.SchemaVersion))
	if err != nil {
		return capability.Grant{}, err
	}
	if err := descriptor.ValidateScope(int(request.SchemaVersion), request.Scope); err != nil {
		return capability.Grant{}, err
	}
	inputs, err := resolver.Scopes(ctx, request)
	if err != nil {
		return capability.Grant{}, err
	}
	for _, scope := range []capability.Scope{inputs.Supported, inputs.Approved, inputs.Policy} {
		if err := descriptor.ValidateScope(int(request.SchemaVersion), scope); err != nil {
			return capability.Grant{}, err
		}
	}
	scope, err := capability.Intersect(request.Name, request.Scope, inputs.Supported, inputs.Approved, inputs.Policy)
	if err != nil {
		return capability.Grant{}, err
	}
	for _, values := range scope.Allowlists {
		if len(values) == 0 {
			return capability.Grant{}, refusal(capability.CapabilityDenied, request.Name)
		}
	}
	for _, limit := range scope.Limits {
		if limit == 0 {
			return capability.Grant{}, refusal(capability.CapabilityDenied, request.Name)
		}
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return capability.Grant{}, refusal(capability.InternalError, request.Name)
	}
	grant := capability.Grant{GrantID: plan.NewGrantID(), Name: request.Name, SchemaVersion: request.SchemaVersion, Scope: raw, HostInstance: plan.Runtime.HostInstance, OwnerID: plan.Runtime.OwnerID, OwnerGeneration: plan.Runtime.OwnerGeneration, Audience: plan.Audience, IssuedAt: plan.IssuedAt, ExpiresAt: plan.ExpiresAt, PolicyRevision: plan.PolicyRevision}
	if grant.Validate() != nil {
		return capability.Grant{}, refusal(capability.InvalidRequest, request.Name)
	}
	return grant, nil
}
