// Package capabilities is the capability vocabulary an agent definition and a
// host negotiate over: what an agent requires (hard), what it merely uses
// (soft), and what a host supports.
//
// The vocabulary is deliberately tiny and open. A name outside the constants
// below is not an error, only unverifiable by static tooling — see [Known].
//
// [Check] reports which required capabilities a host does not support. It takes
// no force flag: overriding an unmet requirement is the caller's decision to
// make and record (contracts.LaunchRecord.Forced), never this package's to
// grant.
//
// This package imports nothing from its parent module; the parent imports it.
package capabilities
