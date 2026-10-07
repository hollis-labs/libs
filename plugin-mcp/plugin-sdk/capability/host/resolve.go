package host

import (
	"context"
	"encoding/json"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
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

// ScopeResolver must honor cancellation and return promptly. Host policy
// remains trusted; callback panic aborts planning rather than hiding as denial.
type ScopeResolver interface {
	Scopes(context.Context, Request) (ScopeInputs, error)
}

// PlanNotice reports either a named refusal (Code set) or an admitted
// narrowing (Narrowed set with GrantID). Narrowing is never silently hidden.
type PlanNotice struct {
	Capability string
	GrantID    string
	Code       capability.Code
	Narrowed   bool
}

// GrantPlan supplies host-issued identity/time/revision metadata and fresh grant
// IDs. It does not allocate incarnations or authenticate the requesting plugin.
type GrantPlan struct {
	Runtime                                       capability.RuntimeIdentity
	Audience, IssuedAt, ExpiresAt, PolicyRevision string
	NewGrantID                                    func() string
}

// ResolveGrants intersects requested, supported, approved and policy authority.
// Optional refusals produce named notices; any required refusal aborts the whole
// plan. No handler/credential/registration is installed. Hosts call it before
// spawn and activation, then serialize the shared Grant DTO into the sole Init.
func ResolveGrants(ctx context.Context, catalog *capability.Catalog, resolver ScopeResolver, requests []Request, plan GrantPlan) (grants capability.GrantSet, notices []PlanNotice, err error) {
	defer func() {
		if recover() != nil {
			grants = nil
			err = refusal(capability.InternalError, "")
		}
	}()
	if err := contextFailure(ctx, ""); err != nil {
		return nil, nil, admissionFailure(err, "", 0)
	}
	if catalog == nil || resolver == nil || plan.NewGrantID == nil || plan.Runtime.Validate() != nil {
		return nil, nil, refusal(capability.InvalidRequest, "")
	}
	grants = capability.GrantSet{}
	notices = []PlanNotice{}
	ids := map[string]bool{}
	for _, request := range requests {
		grant, err := resolveGrant(ctx, catalog, resolver, request, plan)
		if err != nil {
			failure := admissionFailure(err, request.Name, 0)
			// Cancellation and implementation failures must not be hidden as optional
			// feature denial. The host cannot proceed with an incomplete plan.
			if !request.Optional || failure.Code == capability.Cancelled || failure.Code == capability.DeadlineExceeded || failure.Code == capability.InternalError {
				return nil, notices, failure
			}
			notices = append(notices, PlanNotice{Capability: request.Name, Code: failure.Code})
			continue
		}
		if ids[grant.GrantID] {
			return nil, notices, refusal(capability.InternalError, request.Name)
		}
		ids[grant.GrantID] = true
		grants = append(grants, grant)
		effective, err := decodeScope(request.Name, grant.Scope)
		if err != nil {
			return nil, notices, refusal(capability.InternalError, request.Name)
		}
		if capability.CheckNarrowing(request.Name, effective, request.Scope) != nil {
			notices = append(notices, PlanNotice{Capability: request.Name, GrantID: grant.GrantID, Narrowed: true})
		}
	}
	// Validate the metadata even for an empty plan; zero grants convey no authority.
	probe := capability.Grant{GrantID: "validation", Name: capability.ReadonlyQuery, SchemaVersion: 1, Scope: json.RawMessage(`{}`), HostInstance: plan.Runtime.HostInstance, OwnerID: plan.Runtime.OwnerID, OwnerGeneration: plan.Runtime.OwnerGeneration, Audience: plan.Audience, IssuedAt: plan.IssuedAt, ExpiresAt: plan.ExpiresAt, PolicyRevision: plan.PolicyRevision}
	if probe.Validate() != nil {
		return nil, notices, refusal(capability.InvalidRequest, "")
	}
	return grants, notices, nil
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
	original := request
	original.Scope = cloneScope(request.Scope)
	inputs, err := resolver.Scopes(ctx, original)
	if err != nil {
		return capability.Grant{}, err
	}
	if err := contextFailure(ctx, request.Name); err != nil {
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
	id := plan.NewGrantID()
	if !identifier(id) {
		return capability.Grant{}, refusal(capability.InternalError, request.Name)
	}
	grant := capability.Grant{GrantID: id, Name: request.Name, SchemaVersion: request.SchemaVersion, Scope: raw, HostInstance: plan.Runtime.HostInstance, OwnerID: plan.Runtime.OwnerID, OwnerGeneration: plan.Runtime.OwnerGeneration, Audience: plan.Audience, IssuedAt: plan.IssuedAt, ExpiresAt: plan.ExpiresAt, PolicyRevision: plan.PolicyRevision}
	if grant.Validate() != nil {
		return capability.Grant{}, refusal(capability.InvalidRequest, request.Name)
	}
	return grant, nil
}
