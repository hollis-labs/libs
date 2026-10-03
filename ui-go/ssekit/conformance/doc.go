// Package conformance is a reusable raw-wire-format conformance suite for
// anything that parses the WHATWG Server-Sent Events byte stream into
// discrete events. It answers one question only -- does this
// implementation dispatch the same (id, event, data, retry) tuples from
// the same raw bytes, regardless of chunking. It does not test
// reconnection, backoff, idle timeouts or any HTTP-transport behavior;
// that is ssetest's job.
//
// The ground truth is Vectors, the table go-ssekit's own parser is tested
// against. Run drives a ParseFunc through it; an implementation whose shape
// does not fit ParseFunc can range over Vectors itself.
package conformance
