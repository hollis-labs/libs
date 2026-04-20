// Package agentmux provides a Go client for the Agent Mux local HTTP API.
//
// The client supports the daemon's scheme-prefixed listen addresses:
//
//   - unix:/absolute/path
//   - tcp:host:port
//
// It intentionally defines its own public DTOs instead of importing agent-mux
// internal packages, so application repos can depend on this module directly.
package agentmux
