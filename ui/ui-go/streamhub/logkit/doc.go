// Package logkit holds the pieces every durable [streamhub.Log] must share so
// that two backends agree on identity and deduplication: the opaque cursor
// encoding ("<epoch>:<seq>"), the event digest that decides whether a repeated
// Event.ID is a retry or a conflict, and the extractor that lifts indexed scope
// columns out of an event envelope. It is stdlib-only and envelope-agnostic
// at the Log boundary: the extractor is an input to a backend, not part of
// the Log interface.
package logkit
