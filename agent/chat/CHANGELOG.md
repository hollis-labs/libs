# Changelog

## v0.1.0

- Add the conversation wire: `Service` and `NewHandler` serve one durable
  conversation per agent over HTTP and SSE, with chatstream/v1 events on one
  streamhub stream per conversation that does not end when a run does.
- Add the ports a host implements or supplies: `Runner` and `Run`, `Control`
  (with the optional `Bumper`), `Grants`, `Intercept`, `Authorize`, `Clock`.
- Add attachments (viewer or controller, deltas or committed-only), the input
  lease with expiry and takeover, cancel, in-band approvals with scopes and
  expiry, generations, reset, `Service.Submit` from Go and `Service.Observe`
  for runs the runner started.
- Add `Finalizer`: every `part.end` on the stream carries the part's complete
  content in `final`, filled from the deltas when the runner left it empty.
- Add `sqlitestore`, a `streamhub.Log` and the built-in `Control` in one SQLite
  database on `core/store`, and `memstore`, the in-memory `Control`.
- Add `chattest`: `RunnerConformance`, `ControlConformance`, a scripted runner,
  a manual clock and a counting ID source.
- Add `examples/chatserver`, the handler with the scripted runner on localhost.
