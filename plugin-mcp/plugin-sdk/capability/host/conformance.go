package host

// Requirement describes a behavioral integration obligation. Library unit tests
// do not certify a host's transport, authentication, OS or application adapters.
type Requirement struct{ ID, Behavior, Evidence string }

// ConformanceRequirements returns independent metadata for host-owned test
// fixtures. Hosts must report each supported surface and any explicit waiver;
// the descriptor/enforcement library's own cases have no waivers.
func ConformanceRequirements() []Requirement {
	return []Requirement{
		{"C01", "Missing grant contract fails handshake; empty grants deny; optional denial degrades and required denial prevents activation.", "Malformed and empty Init, optional/required planning cases; observe no spawn/registration on required refusal."},
		{"C02", "Unknown name/version/scope and widening fail by name; extensions cannot reinterpret shared descriptors or operations.", "Positive scoped operation plus malformed/version/widening/override attempts."},
		{"C03", "Every direct host RPC or HTTP route checks live authority even when SDK helpers are bypassed.", "Absent/revoked/expired/wrong-audience/wrong-owner binding and target bypass attempts cause zero effects."},
		{"C04", "Forged caller identity, plugin-to-proxy token swapping and cross-generation replay fail; delegated caller is preserved.", "Real authenticated connections/clients and observable downstream/audit identity."},
		{"C05", "Reviewed tool/server/definition/effect, caller policy, cycles and budgets govern list and call.", "Filtered discovery and call attempts after definition/effect/policy changes; concurrency/rate/depth limits and cycles."},
		{"C06", "Disable/stop/reload cancel waiting/in-flight leases and forbid new calls; committed writes are never retried or undone fictionally.", "Controlled waiting/commit races across incarnation replacement and connection loss; exact effects and typed unknown outcome."},
		{"C07", "The non-plugin bridge is literal loopback-only with no redirect/proxy routing, bounded I/O and request-owned cancellation.", "Transport probes for non-loopback/redirect/proxy/oversize/unrelated-cancel/unavailable-host cases."},
		{"C08", "Unsafe install does not widen grants and credentials never enter logs, registry or browser payloads.", "Equivalent scoped calls under normal/unsafe installation and captured-output secret checks."},
		{"C09", "Workflow bindings pin provider/run/step/attempt/fork and narrow deadlines/effects/budgets; revocation stops calls.", "Wrong-context, expired and widened-binding attempts, host-driven steps and negotiated reverse-profile fixtures separately."},
		{"C10", "Each host proves its supported descriptors with positive scoped operations and negative bypass attempts.", "Host-owned operation matrix including actual registration surfaces; unsupported descriptors explicitly refused."},
	}
}
