package mesh_test

import (
	"context"
	"testing"

	"github.com/hollis-labs/substrate/mesh"
	"github.com/hollis-labs/substrate/mesh/fake"
)

func TestBoundedSpawnClaimRequiresItsGuarantees(t *testing.T) {
	d, err := fake.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !d.SupportsSpawn() || d.Validate() != nil {
		t.Fatal("fake lacks valid spawn claim")
	}
	if _, err := mesh.NegotiateCapabilities(d, []mesh.Requirement{{URI: mesh.SpawnCapabilityURI, Verbs: []mesh.Verb{mesh.AgentLaunch}, Modes: []string{"required_limits", "parent_links", "cancel.cascade"}}}); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"required_limits", "parent_links", "cancel.cascade"} {
		copy := d
		copy.Capabilities = append([]mesh.Capability(nil), d.Capabilities...)
		for i, c := range copy.Capabilities {
			if c.URI == mesh.SpawnCapabilityURI {
				copy.Capabilities[i].Modes = nil
				for _, mode := range c.Modes {
					if mode != missing {
						copy.Capabilities[i].Modes = append(copy.Capabilities[i].Modes, mode)
					}
				}
			}
		}
		if copy.Validate() == nil || copy.SupportsSpawn() {
			t.Fatalf("spawn claim accepted without %s", missing)
		}
	}
	for _, missing := range []mesh.Verb{mesh.AgentLaunch, mesh.Cancel} {
		copy := d
		copy.Capabilities = make([]mesh.Capability, len(d.Capabilities))
		for i, capability := range d.Capabilities {
			copy.Capabilities[i] = capability
			copy.Capabilities[i].Verbs = nil
			for _, verb := range capability.Verbs {
				if verb != missing {
					copy.Capabilities[i].Verbs = append(copy.Capabilities[i].Verbs, verb)
				}
			}
		}
		if copy.Validate() == nil || copy.SupportsSpawn() {
			t.Fatalf("spawn claim accepted without %s", missing)
		}
	}
	d.DefaultLimits.Timeout = 0
	if d.Validate() == nil {
		t.Fatal("spawn descriptor accepted unlimited timeout")
	}
}
