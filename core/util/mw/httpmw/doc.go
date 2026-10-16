// Package httpmw adapts the mw contract to net/http. Middleware keep the native
// func(http.Handler) http.Handler signature; the descriptor, phase checks,
// failure modes, shadow mode and Explain come from package mw.
//
// The standard set is Recover, RequestID, AccessLog, BodyLimit, Timeout and
// CORS. Errors are written in the svcerr envelope, so a message is only ever
// the error's safe text. AccessLog never logs headers or query strings.
//
// In Shadow mode the request body is buffered (up to 1 MiB) so the probe and
// the real handler can both read it.
package httpmw
