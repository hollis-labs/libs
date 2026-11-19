// Package httptransport exposes a go-mcp Server over the MCP Streamable
// HTTP transport, per the 2026-07-28 specification, in stateless mode
// (SEP-2567): no Mcp-Session-Id is read or set, and each request is served
// independently. HTTP+SSE, as a dedicated server transport, is not offered;
// Streamable HTTP already carries SSE-formatted responses and notifications
// over the single POST endpoint where the client's Accept header asks for
// them.
//
// Empty host configuration allows only localhost and loopback IP literals.
// Empty origin configuration allows same-origin requests and clients without
// Origin. Configure explicit lists for public hosts and cross-origin clients.
// Guard applies the policy to a whole stack including auth and discovery.
package httptransport
