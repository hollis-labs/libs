package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

// RunSpawn verifies the observable guarantees of the bounded spawn claim.
// Each case receives an isolated provider; no real process should be launched.
func RunSpawn(t *testing.T, factory Factory) {
	tests := map[string]func(*testing.T, Fixture){
		"required_limits_and_parent": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			root := call(mesh.Request{Verb: mesh.AgentLaunch}).Instance
			if err := root.Limits.Validate(); err != nil {
				t.Fatal("unbounded default launch", err)
			}
			expectError(t, f.Provider, mesh.Request{Verb: mesh.AgentLaunch, Limits: mesh.Limits{MaxDepth: -1}}, mesh.ErrorInvalid)
			wide := root.Limits
			wide.MaxDepth++
			expectError(t, f.Provider, mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: wide}, mesh.ErrorLimit)
			child := call(mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN}).Instance
			if child.Parent != root.URN || child.Limits.Validate() != nil {
				t.Fatal("spawn lost parent or effective limits")
			}
			call(mesh.Request{Verb: mesh.Cancel, Target: root.URN, Cascade: true})
			if call(mesh.Request{Verb: mesh.AgentStatus, Target: child.URN}).Instance.SessionState != mesh.SessionEnded {
				t.Fatal("cascade left child active")
			}
		},
		"depth_and_lifetime_children": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			limits := mesh.Limits{MaxDepth: 1, MaxChildren: 2, FanOut: 2, Budget: 8, Timeout: time.Minute}
			root := call(mesh.Request{Verb: mesh.AgentLaunch, Limits: limits}).Instance
			childLimits := limits
			childLimits.Budget = 2
			child := call(mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: childLimits}).Instance
			expectError(t, f.Provider, mesh.Request{Verb: mesh.AgentLaunch, Parent: child.URN, Limits: childLimits}, mesh.ErrorLimit)
			call(mesh.Request{Verb: mesh.Cancel, Target: child.URN})
			call(mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: childLimits})
			expectError(t, f.Provider, mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: childLimits}, mesh.ErrorLimit)
		},
		"fanout_releases_but_budget_does_not": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			limits := mesh.Limits{MaxDepth: 2, MaxChildren: 4, FanOut: 1, Budget: 10, Timeout: time.Minute}
			root := call(mesh.Request{Verb: mesh.AgentLaunch, Limits: limits}).Instance
			childLimits := limits
			childLimits.Budget = 4
			child := call(mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: childLimits}).Instance
			expectError(t, f.Provider, mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: childLimits}, mesh.ErrorLimit)
			call(mesh.Request{Verb: mesh.Cancel, Target: child.URN})
			child = call(mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: childLimits}).Instance
			call(mesh.Request{Verb: mesh.Cancel, Target: child.URN})
			childLimits.Budget = 3
			expectError(t, f.Provider, mesh.Request{Verb: mesh.AgentLaunch, Parent: root.URN, Limits: childLimits}, mesh.ErrorLimit)
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			f := factory(t)
			d, err := f.Provider.Describe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !d.Supports(mesh.AgentLaunch) {
				t.Skip("provider does not launch")
			}
			if !d.SupportsSpawn() {
				t.Fatal("launch provider lacks bounded spawn claim")
			}
			run(t, f)
		})
	}
}
