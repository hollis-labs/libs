// Package fakeserver is a scripted upstream for testing decoders and stream
// clients against the failures a real provider produces: the connection cut
// mid-stream (drop), a reconnect that replays events the client already has
// (overlap), one that skips some (gap), a resume answered 404, and a server that
// goes silent (stuck).
//
// It is a thin layer over go-ssekit's ssetest.Script, which owns the hostile SSE
// server. What this package adds is the vocabulary of this module: a Plan is
// built from chatstream.Frames (a recorded stream, or conformance.Fixture
// frames), each frame served as one SSE event whose id is its position, and a
// handful of ready-made scenarios (DropAfter, Overlap, Gap, ResumeNotFound,
// Stuck) name the situations sketch §7 asks the kit to cover.
//
// A Plan can be served two ways. Serve starts a real httptest server. Memory
// runs the same script behind an in-memory http.Client with no sockets, which is
// what a testing/synctest bubble needs: with real sockets, blocked network reads
// are not "durably blocked" and fake time never advances, but a pipe is.
//
// Every Upstream records its requests, so a test can assert what the client sent
// (the Last-Event-ID of each attempt) as well as what it received.
//
// DecodeOverHTTP is the smallest end-to-end path: GET, SSE framing, decoder. It
// does not reconnect; reconnecting with a cursor is go-ssekit's Client, and the
// package example shows one composed with a decoder.
package fakeserver
