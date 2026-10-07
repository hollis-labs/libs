package host

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
)

type scopeFunc func(context.Context, Request) (ScopeInputs, error)

func (f scopeFunc) Scopes(ctx context.Context, request Request) (ScopeInputs, error) {
	return f(ctx, request)
}
func planningFixture(t *testing.T) (*capability.Catalog, Request, GrantPlan) {
	t.Helper()
	catalog, err := capability.NewCatalog([]string{capability.StorageRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := capability.Scope{Allowlists: map[string][]string{"operations": {"host/storage/get"}, "targets": {"owner"}, "effects": {"read"}, "keys": {"one", "two"}}, Limits: map[string]int64{"response_bytes": 100}}
	request := Request{Name: capability.StorageRead, SchemaVersion: 1, Scope: scope}
	serial := 0
	plan := GrantPlan{Runtime: capability.RuntimeIdentity{HostInstance: "epoch", OwnerID: "owner", OwnerGeneration: 1}, Audience: "stdio", IssuedAt: "2030-01-01T00:00:00Z", ExpiresAt: "2030-01-01T00:05:00Z", PolicyRevision: "review", NewGrantID: func() string {
		serial++
		if serial == 1 {
			return "first"
		}
		return "second"
	}}
	return catalog, request, plan
}
func approvedScopes(_ context.Context, r Request) (ScopeInputs, error) {
	return ScopeInputs{cloneScope(r.Scope), cloneScope(r.Scope), cloneScope(r.Scope)}, nil
}
func TestGrantResolutionUsesSharedDTOAndNarrowing(t *testing.T) {
	catalog, request, plan := planningFixture(t)
	resolver := scopeFunc(func(_ context.Context, r Request) (ScopeInputs, error) {
		inputs, _ := approvedScopes(context.Background(), r)
		inputs.Approved.Allowlists["keys"] = []string{"one"}
		inputs.Policy.Limits["response_bytes"] = 50
		return inputs, nil
	})
	grants, denials, err := ResolveGrants(context.Background(), catalog, resolver, []Request{request}, plan)
	if err != nil || len(grants) != 1 || len(denials) != 1 || !denials[0].Narrowed || denials[0].GrantID != grants[0].GrantID {
		t.Fatalf("resolution: %v", err)
	}
	if err := grants.ValidateForRuntime(plan.Runtime); err != nil {
		t.Fatal(err)
	}
	if grants[0].Name != request.Name || grants[0].SchemaVersion != request.SchemaVersion || grants[0].Audience != plan.Audience || grants[0].PolicyRevision != plan.PolicyRevision {
		t.Fatal("shared grant metadata drift")
	}
	var scope capability.Scope
	json.Unmarshal(grants[0].Scope, &scope)
	if !scope.Allows("keys", "one") || scope.Allows("keys", "two") || scope.Limits["response_bytes"] != 50 {
		t.Fatal("resolved grant widened or ignored policy")
	}
}
func TestOptionalAndRequiredPlanningRefusal(t *testing.T) {
	catalog, request, plan := planningFixture(t)
	unsupported := request
	unsupported.Name = "host.example.unknown"
	unsupported.Optional = true
	grants, denials, err := ResolveGrants(context.Background(), catalog, scopeFunc(approvedScopes), []Request{unsupported, request}, plan)
	if err != nil || len(grants) != 1 || len(denials) != 1 || denials[0].Capability != unsupported.Name || denials[0].Code != capability.UnsupportedCapability {
		t.Fatal("optional denial not visible")
	}
	unsupported.Optional = false
	grants, _, err = ResolveGrants(context.Background(), catalog, scopeFunc(approvedScopes), []Request{request, unsupported}, plan)
	failureCode(t, err, capability.UnsupportedCapability)
	if grants != nil {
		t.Fatal("required denial returned partial grant plan")
	}
	grants, _, err = ResolveGrants(context.Background(), catalog, scopeFunc(approvedScopes), nil, plan)
	if err != nil || grants == nil || len(grants) != 0 {
		t.Fatal("empty plan ambiguous")
	}
	raw, err := json.Marshal(grants)
	if err != nil || string(raw) != "[]" {
		t.Fatal("empty grant contract not explicit")
	}
}
func TestPlanningUnknownScopeVersionAndZeroAuthority(t *testing.T) {
	for _, change := range []func(*Request){
		func(r *Request) { r.SchemaVersion = 2 },
		func(r *Request) { r.Scope.Allowlists["unknown"] = []string{"ambient"} },
		func(r *Request) { r.Scope.Allowlists["keys"] = []string{} },
		func(r *Request) { r.Scope.Limits["response_bytes"] = 0 },
	} {
		catalog, request, plan := planningFixture(t)
		change(&request)
		grants, _, err := ResolveGrants(context.Background(), catalog, scopeFunc(approvedScopes), []Request{request}, plan)
		if err == nil || grants != nil {
			t.Fatal("invalid request activated")
		}
	}
	catalog, request, plan := planningFixture(t)
	request.Optional = true
	resolver := scopeFunc(func(context.Context, Request) (ScopeInputs, error) {
		return ScopeInputs{}, refusal(capability.InternalError, request.Name)
	})
	_, _, err := ResolveGrants(context.Background(), catalog, resolver, []Request{request}, plan)
	failureCode(t, err, capability.InternalError)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = ResolveGrants(ctx, catalog, scopeFunc(approvedScopes), []Request{request}, plan)
	failureCode(t, err, capability.Cancelled)
	plan.NewGrantID = func() string { return "duplicate" }
	request.Optional = false
	_, _, err = ResolveGrants(context.Background(), catalog, scopeFunc(approvedScopes), []Request{request, request}, plan)
	failureCode(t, err, capability.InternalError)
}
func TestPlanningRepeatedNamesKeepOperationTargetPairsSeparate(t *testing.T) {
	catalog, one, plan := planningFixture(t)
	one.Scope.Allowlists["keys"] = []string{"one"}
	two := one
	two.Scope = cloneScope(one.Scope)
	two.Scope.Allowlists["keys"] = []string{"two"}
	grants, _, err := ResolveGrants(context.Background(), catalog, scopeFunc(approvedScopes), []Request{one, two}, plan)
	if err != nil || len(grants) != 2 || grants[0].GrantID == grants[1].GrantID {
		t.Fatal("distinct pair grants collapsed")
	}
}
func TestPlanningMetadataCheckedForEmptyPlan(t *testing.T) {
	catalog, _, plan := planningFixture(t)
	plan.ExpiresAt = time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, _, err := ResolveGrants(context.Background(), catalog, scopeFunc(approvedScopes), nil, plan); err == nil {
		t.Fatal("invalid empty plan metadata accepted")
	}
}

func TestEveryPlanningInputNarrowsGrant(t *testing.T) {
	for _, plane := range []string{"requested", "supported", "approved", "policy"} {
		t.Run(plane, func(t *testing.T) {
			catalog, request, plan := planningFixture(t)
			if plane == "requested" {
				request.Scope.Allowlists["keys"] = []string{"one"}
			}
			resolver := scopeFunc(func(_ context.Context, r Request) (ScopeInputs, error) {
				inputs, _ := approvedScopes(context.Background(), r)
				var scope *capability.Scope
				switch plane {
				case "supported":
					scope = &inputs.Supported
				case "approved":
					scope = &inputs.Approved
				case "policy":
					scope = &inputs.Policy
				}
				if scope != nil {
					scope.Allowlists["keys"] = []string{"one"}
				}
				return inputs, nil
			})
			grants, _, err := ResolveGrants(context.Background(), catalog, resolver, []Request{request}, plan)
			if err != nil || len(grants) != 1 {
				t.Fatal(err)
			}
			var scope capability.Scope
			if err := json.Unmarshal(grants[0].Scope, &scope); err != nil {
				t.Fatal(err)
			}
			if !scope.Allows("keys", "one") || scope.Allows("keys", "two") {
				t.Fatal("planning input ignored")
			}
		})
	}
}
func TestCancelledEmptyPlanDoesNotProceed(t *testing.T) {
	catalog, _, plan := planningFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := ResolveGrants(ctx, catalog, scopeFunc(approvedScopes), nil, plan)
	failureCode(t, err, capability.Cancelled)
}

func TestPlanningCallbackPanicAndGeneratorFailureAbortOptional(t *testing.T) {
	for _, problem := range []string{"scope panic", "generator panic", "blank ID", "invalid UTF-8 ID"} {
		t.Run(problem, func(t *testing.T) {
			catalog, request, plan := planningFixture(t)
			request.Optional = true
			resolver := scopeFunc(approvedScopes)
			switch problem {
			case "scope panic":
				resolver = scopeFunc(func(context.Context, Request) (ScopeInputs, error) { panic("policy") })
			case "generator panic":
				plan.NewGrantID = func() string { panic("ID") }
			case "blank ID":
				plan.NewGrantID = func() string { return "" }
			case "invalid UTF-8 ID":
				plan.NewGrantID = func() string { return string([]byte{0xff}) }
			}
			grants, _, err := ResolveGrants(context.Background(), catalog, resolver, []Request{request}, plan)
			failureCode(t, err, capability.InternalError)
			if grants != nil {
				t.Fatal("broken implementation returned plan")
			}
		})
	}
}

func TestCancellationMidOptionalPlanAborts(t *testing.T) {
	catalog, request, plan := planningFixture(t)
	request.Optional = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolver := scopeFunc(func(ctx context.Context, r Request) (ScopeInputs, error) { cancel(); return approvedScopes(ctx, r) })
	grants, notices, err := ResolveGrants(ctx, catalog, resolver, []Request{request}, plan)
	failureCode(t, err, capability.Cancelled)
	if grants != nil || len(notices) != 0 {
		t.Fatal("cancelled planning became optional denial")
	}
}

func TestPlanningBadOperationRefusedByName(t *testing.T) {
	catalog, request, plan := planningFixture(t)
	supported := cloneScope(request.Scope)
	request.Scope.Allowlists["operations"] = []string{"host/storage/get", "host/storage/delete"}
	resolver := scopeFunc(func(context.Context, Request) (ScopeInputs, error) {
		return ScopeInputs{cloneScope(supported), cloneScope(supported), cloneScope(supported)}, nil
	})
	grants, _, err := ResolveGrants(context.Background(), catalog, resolver, []Request{request}, plan)
	failureCode(t, err, capability.ScopeDenied)
	var failure *capability.Error
	if !errors.As(err, &failure) || failure.Capability != request.Name || grants != nil {
		t.Fatal("bad operation was silently clipped")
	}
}
