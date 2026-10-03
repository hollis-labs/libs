package mesh

import "fmt"

// SpawnCapabilityURI claims bounded spawning with recorded parent links and
// cascading cancellation. Authority to spawn an agent remains host policy.
const SpawnCapabilityURI = "urn:hollis-labs:mesh:spawn/v1"

// DeliveryPolicy specifies when a transport injects a message.
type DeliveryPolicy string

// DeliveryAtIdle queues a reply for the recipient's next turn. A transport
// unable to honor this policy must refuse rather than inject immediately.
const DeliveryAtIdle DeliveryPolicy = "at_idle"

// ValidateSpawnCapabilities rejects incomplete bounded-spawn claims.
func ValidateSpawnCapabilities(d Descriptor) error {
	for _, c := range d.Capabilities {
		if c.URI != SpawnCapabilityURI {
			continue
		}
		launch := false
		for _, v := range c.Verbs {
			launch = launch || v == AgentLaunch
		}
		modes := map[string]bool{}
		for _, mode := range c.Modes {
			modes[mode] = true
		}
		if !launch || !d.Supports(Cancel) || !modes["required_limits"] || !modes["parent_links"] || !modes["cancel.cascade"] {
			return fmt.Errorf("spawn capability requires launch, bounded limits, parent links and cascading cancellation")
		}
	}
	return nil
}

// SupportsSpawn reports an explicit, complete bounded-spawn capability claim.
func (d Descriptor) SupportsSpawn() bool {
	for _, c := range d.Capabilities {
		if c.URI == SpawnCapabilityURI {
			return ValidateSpawnCapabilities(d) == nil
		}
	}
	return false
}
