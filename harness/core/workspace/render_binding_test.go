package workspace_test

import (
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
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
			t.Run(string(provider)+"/"+string(mode), func(t *testing.T) {
				s, c, r, o := planInputs(t)
				req := render.Request{Provider: provider, Layer: layout.Boot, Mode: mode, Agent: "fixture", Roots: c.Roots, Credentials: render.CredentialAvailable}
				for _, field := range []layout.Field{layout.Instructions, layout.Permissions, layout.MCP} {
					resolution, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: provider, Layer: layout.Boot, Mode: mode, Field: field}, Requirement: layout.Required, Components: map[string]string{"agent": "fixture"}, Posture: permission.ModeDefault, LookupPosture: func(p runtimes.ID, m permission.Mode, mode runtimes.Mode) error {
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
			})
		}
	}
}
