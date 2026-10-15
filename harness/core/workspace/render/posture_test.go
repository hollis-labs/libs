package render_test

import (
	"encoding/json"
	"fmt"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/adapters/registry"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"reflect"
	"strings"
	"testing"
)

// The compiler's bound mode is the one input to native policy and the runtime
// reference. Check every supported cell against the actual runtime mapper so
// native config can never silently relax the posture carried by that reference.
func TestNativePostureMatchesRuntimeReference(t *testing.T) {
	for _, provider := range []runtimes.ID{runtimes.Claude, runtimes.Codex, runtimes.OpenCode, runtimes.Antigravity} {
		descriptor, _ := registry.Lookup(string(provider))
		for _, mode := range descriptor.NativeModes() {
			if _, err := layout.For(provider, layout.Boot, mode, ""); err != nil {
				continue
			}
			for _, binding := range permission.ProfileBindings() {
				t.Run(string(provider)+"/"+string(mode)+"/"+binding.Name, func(t *testing.T) {
					bound, err := permission.BindProfile(binding.Name, permission.Ceiling{Modes: []permission.Mode{binding.Mode}})
					if err != nil {
						t.Fatal(err)
					}
					key := layout.Key{Provider: provider, Layer: layout.Boot, Mode: mode, Field: layout.Permissions}
					resolution, err := layout.Resolve(layout.Request{Key: key, Requirement: layout.Required, Posture: bound.Mode, LookupPosture: func(p runtimes.ID, m permission.Mode, r runtimes.Mode) error {
						_, e := descriptor.PostureFor(m, r)
						return e
					}})
					if err != nil {
						t.Fatal(err)
					}
					out, err := render.Render(render.Request{Provider: provider, Layer: layout.Boot, Mode: mode, Agent: "fixture", Roots: map[layout.Root]string{layout.RootBoot: "/fixture/boot", layout.RootProject: "/fixture/project"}, Inputs: []render.Input{{Resolved: resolution}}, Credentials: render.CredentialAvailable})
					if err != nil {
						t.Fatal(err)
					}
					if out.Binding.Posture == nil || out.Binding.Posture.Posture != bound.Mode {
						t.Fatal("runtime reference diverged")
					}
					runtime, err := descriptor.PostureFor(bound.Mode, mode)
					if err != nil {
						t.Fatal(err)
					}
					var body []byte
					for _, e := range out.Tree.Entries {
						if e.Path == resolution.Row.Path {
							body = e.Bytes
						}
					}
					switch provider {
					case runtimes.Claude:
						var doc struct {
							Permissions struct {
								DefaultMode string `json:"defaultMode"`
							} `json:"permissions"`
						}
						if err = json.Unmarshal(body, &doc); err != nil {
							t.Fatal(err)
						}
						assertNoMorePermissive(t, "claude", doc.Permissions.DefaultMode, runtime.Args[1])
						if doc.Permissions.DefaultMode != runtime.Args[1] {
							t.Fatal("native permission differs from runtime", string(body))
						}
					case runtimes.Codex:
						for _, arg := range runtime.Args {
							if arg == "-c" {
								continue
							}
							parts := strings.SplitN(arg, "=", 2)
							native := strings.SplitN(strings.SplitN(string(body), parts[0]+" = ", 2)[1], "\n", 2)[0]
							assertNoMorePermissive(t, parts[0], strings.Trim(native, "\""), strings.Trim(parts[1], "\""))
							if !strings.Contains(string(body), fmt.Sprintf("%s = %s\n", parts[0], parts[1])) {
								t.Fatal("native permission differs from runtime", string(body))
							}
						}
					case runtimes.OpenCode:
						var doc map[string]json.RawMessage
						json.Unmarshal(body, &doc)
						var native, expected map[string]string
						json.Unmarshal(doc["permission"], &native)
						json.Unmarshal([]byte(runtime.Env[registry.OpenCodePermissionEnv]), &expected)
						for key, value := range expected {
							assertNoMorePermissive(t, "opencode", native[key], value)
						}
						if !reflect.DeepEqual(native, expected) {
							t.Fatal("native permission differs from runtime", string(body))
						}
					case runtimes.Antigravity:
						if len(out.Tree.Entries) != 0 || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "native_permission_omitted" || out.Diagnostics[0].Posture != bound.Mode {
							t.Fatal("runtime-only posture must state native absence", out)
						}
					}
					if len(out.Binding.Argv) > 0 && provider == runtimes.Antigravity {
						t.Fatal("runtime-only row leaked argv")
					}
				})
			}
		}
	}
}

// Increasing rank is less restrictive. Approval and filesystem access are
// independent axes: neither may become more permissive than its runtime value.
var permissiveness = map[string]map[string]int{
	"claude":          {"plan": 0, "default": 1, "acceptEdits": 2, "bypassPermissions": 3},
	"sandbox_mode":    {"read-only": 0, "workspace-write": 1, "danger-full-access": 2},
	"approval_policy": {"on-request": 0, "on-failure": 1, "never": 2},
	"opencode":        {"deny": 0, "ask": 1, "allow": 2},
}

func assertNoMorePermissive(t *testing.T, axis, native, runtime string) {
	t.Helper()
	n, nok := permissiveness[axis][native]
	r, rok := permissiveness[axis][runtime]
	if !nok || !rok || n > r {
		t.Fatalf("%s native %q exceeds runtime %q", axis, native, runtime)
	}
}
func TestYoloCannotClaimDenyEnforcement(t *testing.T) {
	if _, err := permission.BindProfile("yolo", permission.Ceiling{Modes: []permission.Mode{permission.ModeYolo}, RequiresDenyEnforcement: true}); err == nil {
		t.Fatal("yolo claimed deny enforcement")
	}
	binding, err := permission.BindProfile("yolo", permission.Ceiling{Modes: []permission.Mode{permission.ModeYolo}})
	if err != nil || !binding.SkipsDenyRules {
		t.Fatal("yolo must explicitly skip deny rules", binding, err)
	}
}
