// Package plugin defines the universal plugin SDK surface that both a
// host application and its plugins import.
//
// The package is host-neutral: it has zero dependencies on any specific
// product code path. A host extends the base contract here with
// host-specific registration methods in its own public package; a
// plugin imports this package directly.
//
// Layout:
//
//   - github.com/hollis-labs/plugin-sdk
//     Plugin, Host, CRUDHandler, EventHook, UIComponent, errors,
//     EnvelopeOut/MessageOut, Logger.
//
//   - github.com/hollis-labs/plugin-sdk/subprocess
//     Subprocess server library (Serve), JSON-RPC 2.0 wire protocol,
//     plugin-side capability interfaces (CommandHandler,
//     EventHandler, MCPHandler, HTTPHandler, Migrator, ...), and
//     stderr JSON-lines logging.
//
//   - github.com/hollis-labs/plugin-sdk/subprocess/subprocesstest
//     In-process test harness for driving plugins without spawning a
//     subprocess.
//
// See the examples directory for runnable plugins, and the README for
// versioning and release status.
package plugin
