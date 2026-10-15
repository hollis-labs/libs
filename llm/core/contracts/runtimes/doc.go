// Package runtimes is the canonical vocabulary for agent CLI runtimes: which
// runtimes exist ([ID]), the launch modes a runtime can be driven in ([Mode]),
// and the runtime capabilities a launcher may rely on ([Capability]).
//
// It is the one spelling every library and app shares (D-73). The per-runtime
// facts — binary, aliases, which modes and capabilities each runtime actually
// has, where it reads its files — are not here: they live in the go-providers
// descriptor registry, which imports this package. Permission posture is not
// here either; it reuses go-permission's Mode (D-72).
//
// Every enum is closed. There are no aliases and no legacy spellings: the
// wrapper's "app-server" and "serve-http", agentkit's "subprocess" and
// go-providers' composite "claude-print" style modes have no constants here and
// are not Valid (D-22). A host that receives a spelling from outside normalizes
// it at its own boundary.
//
// This package imports only the standard library, and nothing from its parent
// module.
package runtimes
