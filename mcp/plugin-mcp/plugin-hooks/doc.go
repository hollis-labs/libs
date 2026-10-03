// Package pluginhooks provides a dependency-free, host-owned hook catalog and
// generation-scoped registration registry. The trusted host constructs Registry
// and Scope; plugins receive only their scope capability. Scope allowlists,
// explicit declaration policies, private catalog copies, opaque handles and
// bounded disposal keep registration authority separate from execution.
//
// AddAction and AddFilter retain context-aware callbacks. This registry-only
// revision does not expose dispatch or advertise executable modes. The internal
// lifecycle supports stable priority order, snapshot isolation, atomic once
// claims, cooperative removal and invalidation of late results after unload.
// Execution capacity must be acquired before a claim and held until actual
// handler completion. Plugin code must never run under registry locks.
//
// Hosts own schema compilation, identity, authorization, transport and shutdown
// budgets. This module does not import plugin-sdk or go-hooks, normalize legacy
// event aliases, migrate consumers or supply implicit policy presets.
package pluginhooks
