package workspace_test

import (
	"fmt"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/adapters/registry"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"testing"
)

func TestRealRendererBindingsPlan(t *testing.T) {
	for _, provider := range []runtimes.ID{runtimes.Claude, runtimes.Codex, runtimes.OpenCode, runtimes.Antigravity} {
		descriptor, _ := registry.Lookup(string(provider))
		for _, mode := range descriptor.NativeModes() {
			if _, err := layout.For(provider, layout.Boot, mode, ""); err != nil {
				continue
			}
			for _, posture := range []permission.Mode{"", permission.ModeDefault} {
				profile := string(posture)
				if profile == "" {
					profile = "absent"
				}
				t.Run(string(provider)+"/"+string(mode)+"/"+profile, func(t *testing.T) {
					s, c, r, o := planInputs(t)
					req := render.Request{Provider: provider, Layer: layout.Boot, Mode: mode, Agent: "fixture", Roots: c.Roots, Credentials: render.CredentialAvailable}
					if posture == "" && provider == runtimes.Codex {
						req.Native.Codex.ApprovalPolicy = "on-request"
						req.Native.Codex.SandboxMode = "read-only"
					}
					for _, field := range []layout.Field{layout.Instructions, layout.Permissions, layout.MCP} {
						resolution, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: provider, Layer: layout.Boot, Mode: mode, Field: field}, Requirement: layout.Required, Components: map[string]string{"agent": "fixture"}, Posture: posture, LookupPosture: func(p runtimes.ID, m permission.Mode, mode runtimes.Mode) error {
							_, err := descriptor.PostureFor(m, mode)
							return err
						}})
						if err != nil {
							t.Fatal(err)
						}
						input := render.Input{Resolved: resolution}
						if field == layout.Instructions {
							input.Content.Body = []byte("fixture instructions\n")
						}
						req.Inputs = append(req.Inputs, input)
					}
					output, err := render.Render(req)
					if err != nil {
						t.Fatal(err)
					}
					c.Rendered = []render.Result{output}
					if _, err := workspace.Plan(s, c, r, o); err != nil {
						t.Fatal(err)
					}
					if output.Binding.Posture != nil {
						for name, change := range map[string]func(*layout.PostureReference){
							"provider": func(p *layout.PostureReference) { p.Provider = "fixture-other" },
							"mapper":   func(p *layout.PostureReference) { p.Mapper = "fixture-other" },
							"mode":     func(p *layout.PostureReference) { p.Posture = "fixture-other" },
						} {
							t.Run(name, func(t *testing.T) {
								bad := *output.Binding.Posture
								change(&bad)
								c.Rendered[0].Binding.Posture = &bad
								_, err := workspace.Plan(s, c, r, o)
								refusal(t, err, workspace.CodeInvalidRenderBinding)
							})
						}
					}
				})
			}
		}
	}
}

func TestRendererOwnershipMetadataHasContentBudget(t *testing.T) {
	s, c, r, o := planInputs(t)
	resolution, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: runtimes.Claude, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Field: layout.MCP}, Requirement: layout.Required})
	if err != nil {
		t.Fatal(err)
	}
	req := render.Request{Provider: runtimes.Claude, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Roots: c.Roots, Inputs: []render.Input{{Resolved: resolution}}}
	for i := 0; i < 200; i++ {
		req.Native.Servers = append(req.Native.Servers, nativefiles.Server{Name: fmt.Sprintf("fixture-%03d", i), Command: "fixture-mcp"})
	}
	output, err := render.Render(req)
	if err != nil {
		t.Fatal(err)
	}
	c.Rendered = []render.Result{output}
	if _, err := workspace.Plan(s, c, r, o); err != nil {
		t.Fatal(err)
	}
}
