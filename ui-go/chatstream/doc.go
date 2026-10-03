// Package chatstream is one canonical vocabulary for chat streams, plus the
// pieces that turn every dialect into it and it into every client.
//
// Providers and protocols stream differently: Anthropic Messages and OpenAI
// Chat and Responses over SSE, ACP over JSON-RPC lines, Claude and Codex CLIs
// over JSON lines, AG-UI and the AI SDK's UI stream toward browsers. This
// module standardizes the seams, not a dialect. Each dialect gets a Decoder
// (package chatstream/adapter/...) that produces Events; each target gets an
// Encoder (chatstream/sink/...) that consumes them; Reduce folds events into a
// Message; and chatstream/conformance ships with all of it.
//
// # Events
//
// An Event is an envelope (V, Seq, RunID, Time, Verb) plus the payload of its
// Verb. The scopes nest: run ⊃ step* ⊃ message ⊃ part*. Provider extras ride in
// Event.Ext, namespaced by provider, and an upstream event the vocabulary has no
// verb for is kept as a raw event beside the standard ones, never instead of
// them. The schema is additive: new verbs, part kinds and optional fields do not
// change SchemaVersion.
//
// # The guarantees
//
//   - Exactly one terminal event (run.finish, run.error or run.abort) ends every
//     run, and nothing follows it.
//   - A decoder never turns truncation into success. When the upstream ends
//     without its terminal signal, the decoder closes what is open and emits
//     run.error with CodeUpstreamTruncated. DecodeFrames enforces this for a
//     whole stream, whatever the upstream does.
//   - Usage is disjoint components (Usage): Total is their sum, so a provider
//     whose prompt count includes cache reads cannot be double-counted.
//   - Capabilities are declared, static and immutable; an adapter never fakes one.
//   - Cursor and gap belong to the hub. Seq is assigned by go-streamhub when an
//     event is published (Event.Seq is that record's Seq), and SSE framing and
//     resume belong to go-ssekit; this module rebuilds neither.
//
// # Using it with a hub
//
// One hub stream carries one session's events, with the event's Verb as the
// streamhub event name:
//
//	payload, _ := json.Marshal(ev)
//	rec, err := hub.Publish(ctx, sessionID, streamhub.Event{Name: string(ev.Verb), Data: payload})
//	// ev.Seq = uint64(rec.Seq): the hub assigns it, chatstream never counts its own.
//
// A client resumes with the Last-Event-ID header only (ssekit.ResumeCursor
// without WithQueryKeys), and a hub gap arrives as a gap event.
package chatstream
