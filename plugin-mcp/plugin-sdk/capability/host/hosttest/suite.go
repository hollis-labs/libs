package hosttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/capability/host"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
)

// Violation identifies an executable requirement/probe that did not pass.
// No missing adapter feature is silently skipped.
type Violation struct{ Requirement, Probe, Reason string }

// Run checks every probe through the adapter. The caller's test timeout also
// applies. Adapters must honor the per-probe context, including during cleanup.
func Run(t *testing.T, a Adapter) {
	t.Helper()
	for _, failure := range Check(context.Background(), a) {
		t.Errorf("%s/%s: %s", failure.Requirement, failure.Probe, failure.Reason)
	}
}

// Check returns failures so a host can prove deliberately broken adapters fail
// without hiding an intentionally failing nested testing.T. It runs the same
// probes as Run; there is no alternate relaxed path.
func Check(ctx context.Context, a Adapter) []Violation {
	if ctx == nil || a == nil {
		return []Violation{{"C01", "adapter", "missing context or adapter"}}
	}
	failures := []Violation{}
	for _, p := range probes() {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := runProbe(bounded, a, p.run); err != nil {
			failures = append(failures, Violation{p.requirement, p.name, err.Error()})
		}
		cancel()
	}
	return failures
}
func runProbe(ctx context.Context, a Adapter, fn func(context.Context, Adapter) error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("adapter panicked")
		}
	}()
	return fn(ctx, a)
}

type probe struct {
	requirement, name string
	run               func(context.Context, Adapter) error
}

func probes() []probe {
	out := []probe{}
	add := func(id, name string, fn func(context.Context, Adapter) error) { out = append(out, probe{id, name, fn}) }
	for _, missing := range []string{"capability_contract", "grants", "null grants", "null contract", "wrong contract"} {
		add("C01", "missing "+missing, func(ctx context.Context, a Adapter) error {
			f, _ := basic(capability.StorageWrite)
			o := new(Observer)
			i, err := a.Open(ctx, f, o)
			if err != nil {
				return err
			}
			defer i.Close()
			var raw map[string]json.RawMessage
			json.Unmarshal(initJSON(f), &raw)
			switch missing {
			case "null grants":
				raw["grants"] = json.RawMessage("null")
			case "null contract":
				raw["capability_contract"] = json.RawMessage("null")
			case "wrong contract":
				raw["capability_contract"] = json.RawMessage("2")
			default:
				delete(raw, missing)
			}
			data, _ := json.Marshal(raw)
			_, _, err = i.Activate(ctx, data, nil)
			if err == nil {
				return errors.New("missing grant contract activated")
			}
			if o.Snapshot().Activations != 0 {
				return errors.New("activation happened before refusal")
			}
			return nil
		})
	}
	add("C01", "empty grants deny", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		f.Grants = capability.GrantSet{}
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			return denied(ctx, i, o, c, capability.CapabilityDenied)
		})
	})
	for _, optional := range []bool{false, true} {
		add("C01", fmt.Sprintf("unknown request optional=%t", optional), func(ctx context.Context, a Adapter) error {
			f, c := basic(capability.StorageWrite)
			o := new(Observer)
			i, err := a.Open(ctx, f, o)
			if err != nil {
				return err
			}
			defer i.Close()
			bad := host.Request{Name: "host.example.unsupported", SchemaVersion: 1, Scope: f.Policy, Optional: optional}
			good := host.Request{Name: c.Call.Capability, SchemaVersion: 1, Scope: f.Policy}
			grants, notices, err := i.Activate(ctx, initJSON(f), []host.Request{bad, good})
			if !optional {
				if err == nil || o.Snapshot().Activations != 0 {
					return errors.New("required refusal activated")
				}
				return namedFailure(err, capability.UnsupportedCapability, bad.Name)
			}
			if err != nil || len(grants) != 1 || len(notices) != 1 || notices[0].Capability != bad.Name || notices[0].Code != capability.UnsupportedCapability || o.Snapshot().Activations != 1 {
				return errors.New("optional denial was not visible or degraded unrelated authority")
			}
			binding, _ := i.Access()
			c.BindingID = binding
			c.Call.GrantID = grants[0].GrantID
			return allowed(ctx, i, o, c)
		})
	}
	for _, problem := range []string{"name", "version", "scope", "widened binding"} {
		add("C02", problem, func(ctx context.Context, a Adapter) error {
			f, c := basic(capability.StorageWrite)
			want := capability.UnsupportedCapability
			switch problem {
			case "name":
				f.Grants[0].Name = "host.example.unknown"
				c.Call.Capability = f.Grants[0].Name
			case "version":
				f.Grants[0].SchemaVersion = 2
			case "scope":
				var s capability.Scope
				json.Unmarshal(f.Grants[0].Scope, &s)
				s.Allowlists["ambient"] = []string{"yes"}
				f.Grants[0].Scope, _ = json.Marshal(s)
				want = capability.ScopeDenied
			case "widened binding":
				want = capability.ScopeDenied
			}
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				if problem == "widened binding" {
					if err := i.Change(ctx, Change{"widen binding", ""}); err != nil {
						return err
					}
				}
				return denied(ctx, i, o, c, want)
			})
		})
	}
	add("C02", "shared descriptor cannot be overwritten", func(ctx context.Context, a Adapter) error {
		f, _ := basic(capability.StorageWrite)
		d := f.Catalog[0]
		d.Name = capability.StorageWrite
		d.EffectCeiling = capability.Read
		f.Overrides = []capability.Descriptor{d}
		o := new(Observer)
		i, err := a.Open(ctx, f, o)
		if i != nil {
			defer i.Close()
		}
		if err == nil {
			return errors.New("shared descriptor override accepted")
		}
		if o.Snapshot().Activations != 0 || o.Snapshot().Executions != 0 {
			return errors.New("override caused activation/effect")
		}
		return namedFailure(err, capability.UnsupportedCapability, capability.StorageWrite)
	})
	for _, problem := range []string{"absent binding", "revoked", "expired", "audience", "owner", "generation", "host epoch", "target"} {
		add("C03", "direct "+problem, func(ctx context.Context, a Adapter) error {
			f, c := basic(capability.StorageWrite)
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				c.Direct = true
				want := capability.CapabilityDenied
				switch problem {
				case "absent binding":
					c.BindingID = "missing"
				case "target":
					c.Call.Target = "other"
					want = capability.ScopeDenied
				default:
					if err := i.Change(ctx, Change{problem, ""}); err != nil {
						return err
					}
					if problem == "audience" {
						want = capability.Unauthenticated
					}
					if problem == "generation" || problem == "host epoch" {
						want = capability.TargetUnavailable
					}
				}
				return denied(ctx, i, o, c, want)
			})
		})
	}
	add("C03", "single grant cannot borrow another scope", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		s := cloneScope(f.Policy)
		s.Allowlists["keys"] = []string{"other"}
		g := f.Grants[0]
		g.GrantID = "other-grant"
		g.Scope, _ = json.Marshal(s)
		f.Grants = append(f.Grants, g)
		f.Policy.Allowlists["keys"] = append(f.Policy.Allowlists["keys"], "other")
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			if err := allowed(ctx, i, o, c); err != nil {
				return err
			}
			second := c
			second.Call.Dimensions = cloneMap(c.Call.Dimensions)
			second.Call.GrantID = "other-grant"
			second.Call.RequestID = 2
			second.Call.Dimensions["keys"] = "other"
			if err := allowed(ctx, i, o, second); err != nil {
				return err
			}
			c.Call.RequestID = 3
			c.Call.Dimensions["keys"] = "other"
			return denied(ctx, i, o, c, capability.ScopeDenied)
		})
	})
	add("C04", "forged caller cannot replace verified caller", delegationProbe(true))
	add("C04", "delegated identity reaches audit", delegationProbe(false))
	for _, dimension := range []string{"sessions", "agents"} {
		add("C04", "forged "+dimension+" cannot replace binding", func(ctx context.Context, a Adapter) error {
			name := capability.ContextSource
			f, c := basic(name)
			c.Call.Dimensions[dimension] = "privileged"
			c.ClaimedCaller = "privileged"
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error { return denied(ctx, i, o, c, capability.ScopeDenied) })
		})
	}
	add("C04", "plugin cannot substitute proxy credential", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			_, c.Credential = i.Access()
			c.BindingID = ""
			return denied(ctx, i, o, c, capability.Unauthenticated)
		})
	})
	add("C04", "replayed generation binding is fenced", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			if err := i.Change(ctx, Change{"replace generation", ""}); err != nil {
				return err
			}
			return denied(ctx, i, o, c, capability.TargetUnavailable)
		})
	})
	for _, problem := range []string{"server", "tool", "effect", "revision", "caller", "cycle", "depth", "bytes", "rate"} {
		add("C05", "MCP "+problem, func(ctx context.Context, a Adapter) error {
			f, c := basic(capability.MCPReach)
			c.Call.Operation = "host/mcp/call_tool"
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				want := capability.ScopeDenied
				switch problem {
				case "server":
					c.Call.Server = "other"
				case "tool":
					c.Call.Tool = "other"
				case "effect":
					if err := i.Change(ctx, Change{"tool effect", "destructive"}); err != nil {
						return err
					}
				case "revision":
					if err := i.Change(ctx, Change{"tool revision", "2"}); err != nil {
						return err
					}
					want = capability.TargetUnavailable
				case "caller":
					if err := i.Change(ctx, Change{"deny caller", ""}); err != nil {
						return err
					}
				case "cycle":
					if err := i.Change(ctx, Change{"cycle", ""}); err != nil {
						return err
					}
				case "depth":
					if err := i.Change(ctx, Change{"depth", ""}); err != nil {
						return err
					}
					want = capability.BudgetExceeded
				case "bytes":
					c.BodyBytes = 1001
					want = capability.BudgetExceeded
				case "rate":
					if err := i.Change(ctx, Change{"rate exhausted", ""}); err != nil {
						return err
					}
					want = capability.RateLimited
				}
				return denied(ctx, i, o, c, want)
			})
		})
	}
	add("C05", "concurrency is reserved atomically", concurrencyProbe)
	add("C05", "read hint cannot authorize a write tool", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.MCPReach)
		f.Policy.Allowlists["effects"] = []string{"read"}
		f.Grants[0].Scope, _ = json.Marshal(f.Policy)
		c.EffectHint = capability.Read
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error { return denied(ctx, i, o, c, capability.ScopeDenied) })
	})
	add("C05", "discovery filters ungranted tools", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.MCPReach)
		c.Call.Operation = "host/mcp/list_tools"
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			reply, err := i.Invoke(ctx, c)
			if err != nil {
				return err
			}
			if reply.Failure != nil || len(reply.Tools) != 1 || reply.Tools[0] != "tool" {
				return errors.New("discovery leaked or hid permitted tools")
			}
			return nil
		})
	})
	for _, event := range []string{"disable", "stop", "reload", "disconnect"} {
		add("C06", event+" before commit", waitingProbe(event, false))
	}
	add("C06", "definite commit survives late revoke", waitingProbe("stop", true))
	add("C06", "unknown write outcome never retries", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		f.FailAfterCommit = true
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			reply, err := i.Invoke(ctx, c)
			if err != nil {
				return err
			}
			if err := validateFailure(reply, c.Call.RequestID); err != nil {
				return err
			}
			s := o.Snapshot()
			if reply.Failure == nil || reply.Failure.Code != capability.UnknownOutcome || reply.Failure.EffectState != capability.Unknown || reply.Failure.Retryable || s.Executions != 1 || s.Reserved != s.Released {
				return errors.New("ambiguous write retried or claimed a definite outcome")
			}
			return nil
		})
	})

	for _, problem := range []string{"origin", "redirect", "proxy", "input", "output", "unavailable"} {
		add("C07", "bridge "+problem, func(ctx context.Context, a Adapter) error {
			f, c := basic(capability.ReadonlyQuery)
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				c.Bridge = true
				_, c.Credential = i.Access()
				c.BindingID = ""
				want := capability.ScopeDenied
				switch problem {
				case "origin":
					c.Origin = "https://example.invalid"
				case "redirect":
					c.Redirect = true
				case "proxy":
					c.Proxy = true
				case "input":
					c.BodyBytes = 1001
					want = capability.BudgetExceeded
				case "output":
					c.ResponseBytes = 1001
					want = capability.BudgetExceeded
				case "unavailable":
					if err := i.Change(ctx, Change{"stop", ""}); err != nil {
						return err
					}
					want = capability.TargetUnavailable
				}
				err := denied(ctx, i, o, c, want)
				if problem == "proxy" && o.Snapshot().ProxyRequests != 0 {
					return errors.New("bridge contacted a proxy")
				}
				return err
			})
		})
	}
	add("C07", "bridge cancellation is request-local", bridgeCancellation)
	add("C08", "unsafe install does not widen grants and artifacts redact", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		f.UnsafeInstall = true
		f.Secret = "fixture-secret-c58f7ea63a09430b"
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			_, token := i.Access()
			c.Call.Target = "other"
			if err := denied(ctx, i, o, c, capability.ScopeDenied); err != nil {
				return err
			}
			artifacts, err := i.Artifacts(ctx)
			if err != nil {
				return err
			}
			for _, surface := range []string{"logs", "registry", "browser"} {
				value, ok := artifacts[surface]
				if !ok || value == "" {
					return fmt.Errorf("missing captured %s", surface)
				}
				if strings.Contains(value, token) || strings.Contains(value, f.Secret) {
					return fmt.Errorf("secret leaked to %s", surface)
				}
			}
			return nil
		})
	})
	for _, problem := range []string{"provider", "run", "step", "attempt", "fork", "effect", "deadline", "budget", "expired", "revoked"} {
		add("C09", "workflow "+problem, func(ctx context.Context, a Adapter) error {
			f, c := basic(workflowName)
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				want := capability.ScopeDenied
				switch problem {
				case "provider", "run", "step", "attempt", "fork":
					c.Call.Dimensions[problem] = "another"
				case "effect":
					c.Call.Operation = "workflow/destructive"
				case "deadline":
					c.Call.Usage["deadline_ms"] = 1001
					want = capability.BudgetExceeded
				case "budget":
					c.BodyBytes = 1001
					want = capability.BudgetExceeded
				case "expired", "revoked":
					if err := i.Change(ctx, Change{problem, ""}); err != nil {
						return err
					}
					want = capability.CapabilityDenied
				}
				return denied(ctx, i, o, c, want)
			})
		})
	}
	for _, name := range []string{capability.ReadonlyQuery, capability.ContextSource, capability.DurableAgentWake, capability.ReflexSeed, capability.MCPReach, capability.StorageWrite, workflowName} {
		add("C10", name+" scoped and direct bypass", func(ctx context.Context, a Adapter) error {
			f, c := basic(name)
			return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
				published, err := i.Descriptors(ctx)
				if err != nil {
					return err
				}
				if len(published) == 0 {
					return errors.New("host published no selected fixture descriptors")
				}
				seen := map[string]bool{}
				for _, d := range published {
					if seen[d.Name] || d.SchemaVersion < 1 || uint64(d.SchemaVersion) > uint64(^uint32(0)) || len(d.Operations) == 0 {
						return errors.New("invalid or duplicate descriptor publication")
					}
					seen[d.Name] = true
					native, nativeCall := descriptorFixture(d)
					if err := withActive(ctx, a, native, nativeCall, func(other Instance, observed *Observer, attempt Attempt) error {
						if err := allowed(ctx, other, observed, attempt); err != nil {
							return err
						}
						attempt.Direct = true
						attempt.Call.Target = "other"
						return denied(ctx, other, observed, attempt, capability.ScopeDenied)
					}); err != nil {
						return fmt.Errorf("published %s: %w", d.Name, err)
					}
				}
				if !seen[name] {
					return errors.New("selected descriptor not published")
				}
				if err := allowed(ctx, i, o, c); err != nil {
					return err
				}
				c.Direct = true
				c.Call.Target = "other"
				return denied(ctx, i, o, c, capability.ScopeDenied)
			})
		})
	}
	add("C10", "audit failure never changes decisions", func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		f.AuditFailure = true
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			if err := allowed(ctx, i, o, c); err != nil {
				return err
			}
			c.Call.Target = "other"
			return denied(ctx, i, o, c, capability.ScopeDenied)
		})
	})
	return out
}

func withActive(ctx context.Context, a Adapter, f Fixture, c Attempt, fn func(Instance, *Observer, Attempt) error) error {
	o := new(Observer)
	i, err := a.Open(ctx, f, o)
	if err != nil {
		return err
	}
	defer i.Close()
	_, _, err = i.Activate(ctx, initJSON(f), nil)
	if err != nil {
		return err
	}
	if o.Snapshot().Activations != 1 {
		return errors.New("fixture did not activate through host")
	}
	c.BindingID, _ = i.Access()
	return fn(i, o, c)
}
func allowed(ctx context.Context, i Instance, o *Observer, c Attempt) error {
	before := o.Snapshot()
	reply, err := i.Invoke(ctx, c)
	if err != nil {
		return err
	}
	after := o.Snapshot()
	if reply.Failure != nil || reply.RPCCode != 0 || after.Executions != before.Executions+1 || after.Reserved != before.Reserved+1 || after.Released != before.Released+1 {
		return errors.New("scoped operation did not execute once with balanced reservation")
	}
	return nil
}
func denied(ctx context.Context, i Instance, o *Observer, c Attempt, want capability.Code) error {
	before := o.Snapshot()
	reply, err := i.Invoke(ctx, c)
	if err != nil {
		return err
	}
	after := o.Snapshot()
	if reply.Failure == nil || reply.RPCCode != capability.HostRPCErrorCode || reply.Failure.Contract != "host-rpc/1" || reply.Failure.Code != want || reply.Failure.RequestID != c.Call.RequestID || reply.Failure.Retryable || reply.Failure.EffectState != capability.NotStarted {
		return fmt.Errorf("expected safe %s refusal", want)
	}
	if err := validateFailure(reply, c.Call.RequestID); err != nil {
		return err
	}
	if after.Executions != before.Executions || after.Reserved-after.Released != before.Reserved-before.Released {
		return errors.New("refusal caused backend execution or leaked reservation")
	}
	return nil
}
func validateFailure(reply Reply, id capability.RequestID) error {
	if reply.Failure == nil || reply.RPCCode != capability.HostRPCErrorCode || reply.Failure.RequestID != id || reply.Failure.Contract != "host-rpc/1" {
		return errors.New("invalid application error envelope")
	}
	if _, err := strictjson.ObjectFields(reply.Data, []string{"contract", "code", "request_id", "effect_state", "retryable"}, []string{"detail"}); err != nil {
		return errors.New("application error data is not closed/non-null")
	}
	var actual capability.RPCErrorData
	if err := json.Unmarshal(reply.Data, &actual); err != nil || actual != *reply.Failure {
		return errors.New("application error data differs from parsed classification")
	}
	classified := &capability.Error{Code: actual.Code, RequestID: actual.RequestID, EffectState: actual.EffectState, Detail: actual.Detail}
	if _, err := classified.RPCData(); err != nil {
		return errors.New("application error classification is invalid")
	}
	return nil
}
func namedFailure(err error, code capability.Code, name string) error {
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != code || e.Capability != name {
		return errors.New("planning/catalog refusal lost symbolic code or name")
	}
	return nil
}
func delegationProbe(forged bool) func(context.Context, Adapter) error {
	return func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		caller := host.Subject{Kind: host.UserActor, ID: "verified-user"}
		f.Caller = &caller
		f.InitIdentity = json.RawMessage(`{"caller":"unrelated-init-user"}`)
		s := cloneScope(f.Policy)
		f.CallerPolicy = &s
		if forged {
			s.Allowlists["keys"] = []string{"different"}
			c.ClaimedCaller = "privileged-user"
		}
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			if forged {
				return denied(ctx, i, o, c, capability.ScopeDenied)
			}
			if err := allowed(ctx, i, o, c); err != nil {
				return err
			}
			snapshot := o.Snapshot()
			if len(snapshot.DeliveredCallers) != 1 || snapshot.DeliveredCallers[0] != caller {
				return errors.New("plugin received the wrong verified caller")
			}
			events := snapshot.Audits
			if len(events) == 0 {
				return errors.New("missing delegated audit")
			}
			event := events[len(events)-1]
			if event.Actor.ID != f.Runtime.OwnerID || event.Actor.Kind != "plugin" || event.InitiatingCaller == nil || event.InitiatingCaller.ID != caller.ID {
				return errors.New("delegated actor/caller not preserved")
			}
			return nil
		})
	}
}
func waitingProbe(event string, afterCommit bool) func(context.Context, Adapter) error {
	return func(ctx context.Context, a Adapter) error {
		f, c := basic(capability.StorageWrite)
		f.Gate = NewGate()
		f.AfterCommit = afterCommit
		defer f.Gate.Release()
		return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
			type result struct {
				reply Reply
				err   error
			}
			done := make(chan result, 1)
			go func() { reply, err := i.Invoke(ctx, c); done <- result{reply, err} }()
			select {
			case <-f.Gate.Entered:
			case <-done:
				return errors.New("call returned without reaching real work barrier")
			case <-ctx.Done():
				return errors.New("call never reached real work barrier")
			}
			if err := i.Change(ctx, Change{event, ""}); err != nil {
				return err
			}
			select {
			case <-f.Gate.Cancelled:
			case <-ctx.Done():
				return errors.New("lifecycle withdrawal did not cancel in-flight lease")
			}
			mid := o.Snapshot()
			if mid.Reserved-mid.Released != 1 {
				return errors.New("running work lost reservation before actual completion")
			}
			f.Gate.Release()
			var got result
			select {
			case got = <-done:
			case <-ctx.Done():
				return errors.New("withdrawn work did not finish")
			}
			end := o.Snapshot()
			if got.err != nil || end.Reserved != end.Released {
				return errors.New("call failed transport or leaked reservation")
			}
			if afterCommit {
				if got.reply.Failure != nil || end.Executions != 1 {
					return errors.New("definite commit was retried, undone or mislabeled")
				}
			} else {
				if got.reply.Failure == nil || end.Executions != 0 || got.reply.Failure.EffectState != capability.NotStarted {
					return errors.New("withdrawal before commit executed")
				}
			}
			return denied(ctx, i, o, c, capability.TargetUnavailable)
		})
	}
}
func bridgeCancellation(ctx context.Context, a Adapter) error {
	f, c := basic(capability.ReadonlyQuery)
	f.Gate = NewGate()
	defer f.Gate.Release()
	return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
		_, c.Credential = i.Access()
		c.Bridge = true
		c.BindingID = ""
		first, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); i.Invoke(first, c) }()
		select {
		case <-f.Gate.Entered:
		case <-done:
			stop()
			return errors.New("bridge call returned without reaching barrier")
		case <-ctx.Done():
			stop()
			return errors.New("bridge call never entered")
		}
		stop()
		select {
		case <-f.Gate.Cancelled:
		case <-ctx.Done():
			return errors.New("bridge request cancellation not delivered")
		}
		f.Gate.Release()
		select {
		case <-done:
		case <-ctx.Done():
			return errors.New("cancelled bridge did not return")
		}
		if err := waitFor(ctx, func() bool { s := o.Snapshot(); return s.Reserved == s.Released }); err != nil {
			return err
		}
		// Same credential/instance must remain usable after cancelling one request.
		c.Call.RequestID = 2
		return allowed(ctx, i, o, c)
	})
}

const workflowName = "host.example.workflow.callback"

func basic(name string) (Fixture, Attempt) {
	d := capability.Descriptor{}
	if name == workflowName {
		d = capability.Descriptor{Name: name, SchemaVersion: 1, Description: "Fixture run binding", EffectCeiling: capability.Write, Operations: []string{"workflow/callback", "workflow/destructive"}, ScopeSchema: capability.ScopeSchema{Allowlists: []string{"operations", "targets", "effects", "provider", "run", "step", "attempt", "fork"}, Limits: []string{"request_bytes", "deadline_ms"}}}
	} else {
		for _, candidate := range capability.SharedDescriptors() {
			if candidate.Name == name {
				d = candidate
				break
			}
		}
	}
	return descriptorFixture(d)
}

func descriptorFixture(d capability.Descriptor) (Fixture, Attempt) {
	name := d.Name
	effect := d.EffectCeiling
	if name == capability.StorageWrite {
		effect = capability.Write
	}
	if name == capability.MCPReach {
		effect = capability.Write
	}
	s := capability.Scope{Allowlists: map[string][]string{"operations": d.Operations, "targets": {"owner"}, "effects": {string(effect)}}, Limits: map[string]int64{}}
	if name == capability.MCPReach {
		s.Allowlists["effects"] = []string{"read", "write"}
	}
	dimensions := map[string]string{}
	for _, key := range d.ScopeSchema.Allowlists {
		if key == "operations" || key == "targets" || key == "effects" {
			continue
		}
		value := key + "-one"
		if key == "server_tools" {
			value = "server/tool@1"
		}
		s.Allowlists[key] = []string{value}
		dimensions[key] = value
	}
	usage := map[string]int64{}
	for _, key := range d.ScopeSchema.Limits {
		s.Limits[key] = 1000
		usage[key] = 1
		if key == "deadline_ms" {
			usage[key] = 500
		}
	}
	raw, _ := json.Marshal(s)
	issued, expiry := fixtureTimes()
	runtime := capability.RuntimeIdentity{HostInstance: "fixture-host", OwnerID: "owner", OwnerGeneration: 1}
	g := capability.Grant{GrantID: "grant", Name: name, SchemaVersion: uint32(d.SchemaVersion), Scope: raw, HostInstance: runtime.HostInstance, OwnerID: runtime.OwnerID, OwnerGeneration: 1, Audience: "stdio", IssuedAt: issued, ExpiresAt: expiry, PolicyRevision: "review"}
	f := Fixture{Runtime: runtime, Grants: capability.GrantSet{g}, Catalog: []capability.Descriptor{d}, Policy: cloneScope(s)}
	call := host.Call{Capability: name, GrantID: g.GrantID, Operation: d.Operations[0], Target: "owner", Effect: effect, Dimensions: dimensions, Usage: usage, RequestID: 1, TraceID: "fixture-trace", Server: "server", Tool: "tool"}
	if name == capability.MCPReach {
		call.Operation = "host/mcp/call_tool"
	}
	return f, Attempt{Call: call, BodyBytes: 1, ResponseBytes: 1}
}
func cloneScope(s capability.Scope) capability.Scope {
	raw, _ := json.Marshal(s)
	var out capability.Scope
	json.Unmarshal(raw, &out)
	return out
}
func initJSON(f Fixture) json.RawMessage {
	fields := map[string]any{"capability_contract": capability.ContractVersion, "grants": f.Grants, "incarnation": f.Runtime, "plugin_dir": "/fixture", "config": map[string]string{}, "data_dir": "/fixture/data", "cache_dir": "/fixture/cache", "log_level": "info", "host_info": map[string]any{"version": "fixture", "protocol": 2}}
	if f.InitIdentity != nil {
		fields["identity"] = f.InitIdentity
	}
	raw, _ := json.Marshal(fields)
	return raw
}

func waitFor(ctx context.Context, predicate func() bool) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return errors.New("fixture work did not settle")
		}
	}
	return nil
}

func concurrencyProbe(ctx context.Context, a Adapter) error {
	f, c := basic(capability.MCPReach)
	f.Policy.Limits["concurrency"] = 1
	f.Grants[0].Scope, _ = json.Marshal(f.Policy)
	f.Gate = NewGate()
	defer f.Gate.Release()
	return withActive(ctx, a, f, c, func(i Instance, o *Observer, c Attempt) error {
		done := make(chan error, 1)
		go func() { done <- allowed(ctx, i, o, c) }()
		select {
		case <-f.Gate.Entered:
		case <-done:
			return errors.New("call did not reserve live work")
		case <-ctx.Done():
			return ctx.Err()
		}
		second := c
		second.Call.RequestID = 2
		// A refusal must return while the first worker still owns its reservation.
		if err := denied(ctx, i, o, second, capability.BudgetExceeded); err != nil {
			return err
		}
		if s := o.Snapshot(); s.Reserved-s.Released != 1 {
			return errors.New("parallel refusal changed live reservation")
		}
		f.Gate.Release()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}
