# core/panics

Panic containment for Go services: recover at the boundary, turn the panic into a
**ref** and a **sanitized record**, and let nothing else cross.

```go
panics.SetDefault(panics.Config{
	Reporter: panics.StderrReporter(os.Stderr), // or your own: log + database row
	Redact:   appRedactor.Text,                // the application's standard redactor
})

panics.Go("reaper", reapLoop, nil)              // never a bare `go func()`

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var err error
	defer panics.Recover("handle "+r.Pattern, &err)
	...
}

mux := httpguard.Handler(mux)                   // net/http: 500 + ref, panic reported
```

A caller gets `internal error (ref pnc_3f9a0c21b7de)`. The operator looks the ref up and
finds the redacted, bounded value and stack.

## Why this exists

An unrecovered panic in **any** goroutine ends the whole process. A recovered panic that
is printed raw leaks whatever the failing code was holding: a credential, a user's query,
a file path. A panic that is merely recovered and swallowed leaves work half-done (a row
stuck "running", a child process still alive). This package makes the three decisions the
same way everywhere: contain it, never show the value, finish the bookkeeping.

## The rules

1. **The panic value is untrusted text.** It is redacted by `Config.Redact`, stripped of
   control and format characters (a hostile value must not repaint a terminal or hide
   behind a bidi override) and bounded, before it is stored. Its own `Error()`/`String()`
   method may panic; that is contained.
2. **No redactor, no value.** This package has no dependencies, so it cannot know what a
   secret looks like. With no `Redact` configured the value is withheld and only its type is
   kept. Recording raw values is an explicit choice: `Redact: panics.Unredacted`.
3. **A ref crosses boundaries; the detail does not.** HTTP responses, MCP/tool errors and CLI
   output carry `internal error (ref …)` and nothing from the panic.
4. **Failing safe is the caller's job.** Code that decides for the caller (a parser, a
   policy, a permission check) must treat a panic as "deny" or "needs a human", never as
   "allow".
5. **Finish the bookkeeping.** An executor that holds a resource (child process, lock,
   "running" row) recovers, releases it and records the failure on the owning record.

## Boundary checklist

| Boundary | Use | Notes |
|---|---|---|
| Every goroutine | `panics.Go` | Never a bare `go func()`. Pass `onPanic` to mark the owner failed or restart a loop. |
| HTTP handler | `httpguard.Handler` | Records the route pattern, never the raw path. `http.ErrAbortHandler` keeps its meaning. |
| Tool call / message / job | `panics.Call` | One bad request must not stop the server. Map the `*panics.Error` to your transport's internal error. |
| Parser, policy, validator | `panics.Call`, then fail closed | Test it: inject a panicking implementation and assert "deny". |
| Executor holding resources | `defer panics.Recover` + cleanup | Record the ref on the run/job record. |
| Callback into a library | `panics.Call` around the callback | Especially writers/handlers a library invokes from its **own** goroutine (os/exec copy goroutines, http, grpc): you cannot recover from outside. |
| `main` | `panics.Recover` in the outermost function | Exit with `panics.ExitSoftware` (70) and a ref, after deferred cleanup runs. |

To find the misses in an existing code base:

```sh
grep -rnE '^\s*go (func|[A-Za-z_.]+)\(' --include='*.go' . | grep -v _test.go
```

Every hit should be `panics.Go`, or sit inside a function that is itself contained.

## Redaction

`Config.Redact` takes a `func(string) string`. Pass the application's standard redactor
(patterns for credentials, tokens, URLs with userinfo; plus known secret values if the
application holds them). It runs over the panic value, the stack and attribute values.
If it panics, the text is withheld rather than recorded raw. `core` cannot import the
`security` group, which is why the redactor is injected and not imported.

## Reporting

A `Reporter` receives each `Record` (`Ref`, `At`, `Where`, `Value`, `Stack`, `Attrs`).
Typical applications log one line and persist the record so an operator can run
`app panics show <ref>`. `panics.Multi` fans out and isolates a failing reporter;
`panics.StderrReporter` is the minimal default. Attach context with
`panics.With("run", id)`.

## Testing

Containment is only real if tested at each boundary. Inject a panic (a store or blob
backend whose `Get` panics, a policy function that panics) and assert: the process
survives, the caller sees only the ref, the record is reported, the owning record is
failed, and for fail-closed code the decision is "deny". Then break the containment and
confirm the test fails.

`go test -fuzz=FuzzCapture` hunts for hostile panic values.
