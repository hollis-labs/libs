package hosttest

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/capability/host"
)

func resourceKey(s capability.Scope) string {
	keys := []string{}
	for key := range s.Allowlists {
		if key != "operations" && key != "targets" && key != "effects" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if slices.Contains(keys, "keys") {
		return "keys"
	}
	if len(keys) > 0 {
		return keys[0]
	}
	return "targets"
}
func extraProbes(descriptor capability.Descriptor) []probe {
	core := descriptor.Name
	fallbackBasic := basic
	basic := func(name string) (Fixture, Attempt) {
		if name == core {
			return descriptorFixture(descriptor)
		}
		return fallbackBasic(name)
	}
	out := []probe{}
	add := func(id, name string, fn func(context.Context, Adapter) error) { out = append(out, probe{id, name, fn}) }
	add("C02", "planning wrong version", func(ctx context.Context, a Adapter) error {
		f, _ := basic(core)
		o := new(Observer)
		i, err := a.Open(ctx, f, o)
		if err != nil {
			return err
		}
		defer i.Close()
		_, _, err = i.Activate(ctx, initJSON(f), []host.Request{{Name: core, SchemaVersion: 7, Scope: f.Policy}})
		if err == nil || o.Snapshot().Activations != 0 {
			return suiteError("version mismatch activated")
		}
		return namedFailure(err, capability.UnsupportedCapability, core)
	})
	add("C02", "planning wider than policy", func(ctx context.Context, a Adapter) error {
		f, _ := basic(core)
		o := new(Observer)
		i, err := a.Open(ctx, f, o)
		if err != nil {
			return err
		}
		defer i.Close()
		wide := cloneScope(f.Policy)
		wide.Allowlists["targets"] = append(wide.Allowlists["targets"], "other")
		grants, notices, err := i.Activate(ctx, initJSON(f), []host.Request{{Name: core, SchemaVersion: uint32(descriptor.SchemaVersion), Scope: wide}})
		if err != nil {
			return err
		}
		if len(grants) != 1 || len(notices) != 1 || !notices[0].Narrowed || notices[0].Capability != core || notices[0].GrantID != grants[0].GrantID {
			return suiteError("missing named narrowing notice")
		}
		var actual capability.Scope
		if json.Unmarshal(grants[0].Scope, &actual) != nil || capability.CheckNarrowing(core, f.Policy, actual) != nil || capability.CheckNarrowing(core, actual, f.Policy) != nil {
			return suiteError("planner widened approved scope")
		}
		return nil
	})
	for _, control := range []ChangeKind{Expire, Revoke} {
		name := "expiry distinct from revocation"
		restore := RestoreExpiry
		if control == Revoke {
			name = "revocation distinct from expiry"
			restore = RestoreRevocation
		}
		add("C03", name, func(ctx context.Context, a Adapter) error {
			f, c := basic(core)
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				if err := allowed(ctx, i, o, c); err != nil {
					return err
				}
				if err := i.Change(ctx, Change{control, ""}); err != nil {
					return err
				}
				if err := denied(ctx, i, o, c, capability.CapabilityDenied); err != nil {
					return err
				}
				if err := i.Change(ctx, Change{restore, ""}); err != nil {
					return err
				}
				c.Call.RequestID = 2
				return allowed(ctx, i, o, c)
			})
		})
	}
	f, _ := basic(core)
	for dim := range f.Policy.Allowlists {
		add("C08", "unsafe dimension "+dim, func(ctx context.Context, a Adapter) error {
			f, c := basic(core)
			f.UnsafeInstall = true
			switch dim {
			case "targets":
				c.Call.Target = "other"
			case "operations":
				if len(f.Catalog[0].Operations) > 1 {
					c.Call.Operation = f.Catalog[0].Operations[1]
					if core == capability.StorageWrite {
						f.Policy.Allowlists["effects"] = []string{"write", "destructive"}
					}
					grantScope := cloneScope(f.Policy)
					grantScope.Allowlists["operations"] = []string{f.Catalog[0].Operations[0]}
					f.Grants[0].Scope, _ = json.Marshal(grantScope)
				} else {
					c.Call.Operation = "ungranted/operation"
				}
			case "effects":
				c.Call.Operation = "workflow/destructive"
				if core == capability.StorageWrite {
					c.Call.Operation = "host/storage/delete"
				}
			default:
				c.Call.Dimensions[dim] = "other"
			}
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error { return denied(ctx, i, o, c, capability.ScopeDenied) })
		})
	}
	for limit := range f.Policy.Limits {
		if limit != "request_bytes" {
			continue
		}
		add("C08", "unsafe limit "+limit, func(ctx context.Context, a Adapter) error {
			f, c := basic(core)
			f.UnsafeInstall = true
			c.BodyBytes = f.Policy.Limits[limit] + 1
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error { return denied(ctx, i, o, c, capability.BudgetExceeded) })
		})
	}
	for _, control := range []ChangeKind{Stop, Revoke, Expire} {
		add("C05", "discovery after "+string(control), func(ctx context.Context, a Adapter) error {
			f, c := basic(capability.MCPReach)
			c.Call.Operation = "host/mcp/list_tools"
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				if err := allowed(ctx, i, o, c); err != nil {
					return err
				}
				if err := i.Change(ctx, Change{control, ""}); err != nil {
					return err
				}
				code := capability.CapabilityDenied
				if control == Stop {
					code = capability.TargetUnavailable
				}
				return denied(ctx, i, o, c, code)
			})
		})
	}
	add("C05", "discovery intersects caller permission", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.MCPReach)
		f.Policy.Allowlists["server_tools"] = []string{"server/tool@1", "server/ungranted-tool@1"}
		f.Grants[0].Scope, _ = json.Marshal(f.Policy)
		caller := host.Subject{Kind: host.UserActor, ID: "verified-user"}
		f.Caller = &caller
		cp := cloneScope(f.Policy)
		cp.Allowlists["server_tools"] = []string{"server/tool@1"}
		f.CallerPolicy = &cp
		c.Call.Operation = "host/mcp/list_tools"
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			before := o.Snapshot()
			r, err := i.Invoke(ctx, c)
			if err != nil {
				return err
			}
			after := o.Snapshot()
			if r.Failure != nil || !reflect.DeepEqual(r.Tools, []string{"tool"}) || after.Executions != before.Executions+1 || after.Reserved-after.Released != before.Reserved-before.Released {
				return suiteError("discovery ignored caller or admission")
			}
			return nil
		})
	})
	out = append(out, secretDiagnosticProbe(descriptor))
	return out
}

func secretDiagnosticProbe(descriptor capability.Descriptor) probe {
	return probe{"C08", "secret diagnostic output", func(ctx context.Context, a Adapter) error {
		f, c := descriptorFixture(descriptor)
		f.Secret = freshSecret()
		f.FailAfterCommit = true
		c.Call.TraceID = f.Secret
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			r, err := i.Invoke(ctx, c)
			if err != nil {
				return err
			}
			if err := validateFailure(r, c.Call.RequestID); err != nil {
				return err
			}
			want := capability.UnknownOutcome
			if c.Call.Effect == capability.Read {
				want = capability.InternalError
			}
			if r.Failure.Code != want || r.Failure.EffectState != capability.Unknown || r.Failure.Retryable {
				return suiteError("diagnostic failure not normalized")
			}
			if o.Snapshot().Executions != 1 {
				return suiteError("diagnostic never exercised backend")
			}
			_, token := i.Access()
			if len(token) < 16 {
				return suiteError("credential leak path unexercised")
			}
			artifacts, err := i.Artifacts(ctx)
			if err != nil {
				return err
			}
			if ContainsSecret(string(r.Wire), token) || ContainsSecret(string(r.Wire), f.Secret) || ContainsSecret(string(r.Data), token) || ContainsSecret(string(r.Data), f.Secret) {
				return suiteError("reply data secret leak")
			}
			inputs := o.Snapshot().SensitiveInputs
			for _, surface := range []string{"logs", "registry", "browser", "audit", "replies", "errors"} {
				value, ok := artifacts[surface]
				if !ok || value == "" || inputs[surface] == 0 {
					return suiteError("sensitive output adaptation unexercised")
				}
				if ContainsSecret(value, token) || ContainsSecret(value, f.Secret) {
					return suiteError("encoded/prefix secret leak")
				}
			}
			return nil
		})
	}}
}

// Unsupported calls use the actual declared catalog, never an adapter waiver.
func unsupportedProbes(supported []capability.Descriptor) []probe {
	names := map[string]bool{}
	for _, d := range supported {
		names[d.Name] = true
	}
	out := []probe{}
	for _, d := range capability.SharedDescriptors() {
		if names[d.Name] {
			continue
		}
		out = append(out, probe{"C10", d.Name + " declared unsupported refuses", func(ctx context.Context, a Adapter) error {
			f, c := descriptorFixture(d)
			f.Catalog = append([]capability.Descriptor{}, supported...)
			f.Grants = capability.GrantSet{}
			// No installed grant can authorize an unsupported descriptor.
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				c.Direct = true
				return denied(ctx, i, o, c, capability.UnsupportedCapability)
			})
		}})
	}
	return out
}
