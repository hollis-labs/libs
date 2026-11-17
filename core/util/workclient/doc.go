// Package workclient is a Go client for the Work service's HTTP API: workspaces of typed
// items (Planner, Content and Writer), their notes and review rounds, saved views and the
// change-event stream.
//
// It speaks the service's /v1/work routes and GET /v1/events with the same request and
// response JSON the service serves, and defines its own types for that JSON, so an
// application depends on this package and nothing else of the service.
//
// # Identity
//
// Every request carries the service key (Config.Token, read on each request so a rotated
// key is picked up), the owner namespace (X-Owner-NS) and a correlation id. A service key
// allowed to delegate may name the signed-in person with X-Principal-URN; any other caller
// may assert an actor with X-Actor. Config sets the defaults; WithOwnerNS, WithPrincipal
// and WithActor override them for one call through its context.
//
// # Errors
//
// A non-2xx answer is an *Error with the service's status, code and message. errors.Is
// reads it against ErrNotFound (404), ErrUnauthorized (401), ErrForbidden (403) and
// ErrUnavailable (503); a conflict carries Conflict (an edit made from a stale revision:
// the service kept it as suggestions) or OpenNotes (a sign-off refused because notes are
// still open). A request that got no answer matches ErrUnreachable, and an answer that
// could not be read or decoded matches ErrBadAnswer.
//
// # Events
//
// EventStream opens the change stream as Server-Sent Events after a sequence (from the
// service's head when since is empty), for a caller that passes it on as it is;
// NewEventReader reads Events from it. Events carry no bodies: read the item they name.
//
// # Compatibility
//
// The Work service is pre-1.0 and its API can change in any minor release; this package
// tracks it.
package workclient
