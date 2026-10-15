// Package tether provides a typed Go client for the Tether daemon's local
// control-plane HTTP API.
//
// The client supports the daemon's scheme-prefixed listen addresses and HTTP(S)
// base URLs:
//
//   - unix:/absolute/path
//   - tcp:host:port
//   - http://host[:port][/base]
//   - https://host[:port][/base]
//
// The package intentionally defines its own public DTOs instead of importing
// Tether internal packages, so application repos can depend on this module
// directly.
//
// The surface covers daemon health, session lifecycle operations, attach/wait,
// checkpoints, catalog reads, messaging routes, event history + SSE streams,
// and the Tether AI gateway endpoints for provider/model/route inspection,
// chat, usage, budgets, and audit.
package tether
