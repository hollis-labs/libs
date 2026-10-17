// Package toolmw adapts the core/util/mw contract to go-mcp tool handlers.
//
// A Stack is a validated set of members. Stack.Middleware returns one
// server.ToolMiddleware to pass to server.WithToolMiddleware, so a server gets
// descriptor checks, phase ordering, failure modes, shadow mode and Explain,
// while go-mcp's own StrictArgs and ValidateSchema keep working through Wrap.
// Each member's Build runs once per tool, at registration, with the tool's
// definition; Member.Applies can limit a member to some tools.
//
// Errors cross the wire as *budget.ToolError. A middleware fault or a recovered
// panic becomes a generic internal error, and a deadline becomes a timeout:
// go-mcp reports any other error's text to the client, and a fault's text is
// the middleware's business.
//
// The standard set is Recover, Audit (tool, duration and outcome; never
// arguments or results), Timeout (with per-tool overrides), Authorize (the
// policy seam) and DenyDestructive. Argument sanitizing is the go-mcp
// sanitize package's job.
package toolmw
