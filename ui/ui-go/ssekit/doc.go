// Package ssekit reads and writes Server-Sent Events (WHATWG HTML, section
// 9.2) without knowing what the events mean.
//
// Every type carries opaque bytes, an event name and an id string. There is no
// event vocabulary, no terminal vocabulary, no cursor arithmetic and no CORS
// policy here: the application decides what an id means, which event ends a
// stream and who may connect.
//
// The server side is a Writer (headers, deadlines, one flush per event, every
// error returned), a Serve loop (heartbeat comments, maximum lifetime, terminal
// event, cancellation) driven by a Source, and ResumeCursor, which extracts the
// client's resume cursor from the Last-Event-ID header and/or query parameters.
// Merge and PollSource build Sources.
//
// The client side is Read, a spec-conforming parser over any io.Reader, and
// Client.Stream, which adds reconnecting with the last event id, backoff, a
// server-directed retry, an idle watchdog and continuity checks.
//
// Package ssetest holds the test harness: a scripted, deliberately hostile
// server and a frame recorder/replayer.
package ssekit
