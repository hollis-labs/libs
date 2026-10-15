// Package policy defines provider-neutral policy evaluation and admission.
// Hosts supply verified identities and facts, and retain ownership of execution,
// approval brokers, counters, credentials and obligation fulfillment. An allow
// decision with obligations is not permission to execute without satisfying them.
//
// This package contains no policy engine, provider adapter or precedence rules.
package policy
