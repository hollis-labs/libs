// Package session coordinates experimental durable conversations through
// authoritative host ports. It owns no database, identity issuer or provider
// process. An operative can have several independent conversations; each has one
// active attempt and at most one input writer, alongside any number of viewers.
//
// Fresh and continue are supported. Relaunch and fork are explicitly unsupported.
// All identities and generations are supplied by the host. Continue requires an
// exact parent binding, including for recovery from a failed fenced transition.
// Provider-native resume identifiers do not establish portable history or plans.
//
// Host implementations must enforce the atomic admission contracts documented on
// Conversations and InputLeases. A Manager's earlier read is never authorization
// for a later delivery. Runtime preparation is inert until authoritative active
// binding admission succeeds. Hosts retain authentication, grants, quotas,
// durable storage, retention and process supervision.
package session
