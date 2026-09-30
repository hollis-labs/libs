// Package hubbind is the thin, documented, tested seam between chatstream
// events and go-streamhub. It is not a hub and not a framing layer: the hub
// assigns the cursor and does replay, gap detection and slow-consumer policy,
// and go-ssekit does SSE framing and Last-Event-ID handling. This package only
// says how a chatstream.Event travels through them.
//
// The pattern, per the wire contract:
//
//   - One hub stream carries one session's events, and the event's Verb is the
//     streamhub event name (so a Filter can select by verb).
//   - Publish stamps Event.Seq from the record the hub returns; chatstream never
//     counts its own.
//   - Terminal is the hub's terminal predicate: after a run.finish, run.error or
//     run.abort the hub ends the stream, and subscribers see EOF.
//   - Events turns a subscription back into events, rendering the hub's gap
//     notices as in-band gap events.
//   - A client resumes with the Last-Event-ID header only: use
//     ssekit.ResumeCursor(r) without WithQueryKeys.
package hubbind
