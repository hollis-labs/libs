// Package svcerr is a small, transport-agnostic carrier for service-layer
// errors that need to cross a boundary (HTTP, in practice, everywhere this was
// found) carrying a status code, a machine-readable code and a safe,
// user-facing message: the three facts that at least nine Hollis Labs apps'
// handler layers each derive by hand today, in at least six mutually
// incompatible JSON shapes.
//
// svcerr does not pick a JSON envelope for every app. What is genuinely shared
// is the carrier — a typed error with a status, a code and a message, matched
// with errors.Is and errors.As — not the wire format around it, which the apps
// have diverged on for real, app-specific reasons. [WriteJSON] is one optional,
// opinionated default for a NEW handler with no envelope of its own yet; an
// existing app keeps whatever shape it writes and adopts only [Error],
// [StatusFor] and [CodeFor] underneath it.
//
// Two rules the package holds to:
//
//   - The message is never derived from a wrapped error. [Error.Message] is an
//     explicit, deliberately safe string; [Error.Err] carries the internal
//     detail for the caller to log and is never rendered into a response.
//   - A status is never guessed from the wording of an error. An error that is
//     not an [*Error] has no status here: [StatusFor] returns the caller's
//     fallback for it.
package svcerr
