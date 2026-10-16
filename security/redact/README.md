# security/redact

Credential redaction for text, JSON, command lines, environments and logs. Standard
library only. It is the one redactor to use where text leaves your control: logs,
stored output, error messages, diagnostics, text sent to a model.

```go
scope := redact.NewScope()             // the secrets you KNOW (resolved credentials)
scope.Add("DB_PASSWORD", dbPassword)

r, err := redact.New(redact.WithScope(scope))   // patterns find the ones you do not
if err != nil { ... }

log := slog.New(r.Handler(slog.NewJSONHandler(os.Stderr, nil)))

r.Text(stderr)                  // "Authorization: Bearer [REDACTED]"
r.JSON(body)                    // valid JSON, order and numbers kept
r.Args(argv)                    // --token [REDACTED], curl -u user:[REDACTED]
r.Env(os.Environ())             // DB_PASSWORD=[REDACTED]
r.ErrorText(err)                // prose kept, cause redacted (see Guidance)
redact.SensitiveName("PGPASSWORD") // the one predicate for "does this name hold a secret"
```

## What it protects

| Class | Examples |
|---|---|
| Provider keys | `sk-`, `sk-ant-`, `sk_live_`, `ghp_`/`github_pat_`, `xox*`, `AKIA`/`ASIA`, `glpat-`, `AIza`, `npm_`, `SG.`, `ya29.`, `hf_`, JWTs, webhook URLs |
| Headers | `Authorization` / `Proxy-Authorization` (scheme kept), bare `Bearer x`, `Cookie` / `Set-Cookie` values (names and attributes kept) |
| Private keys | PEM blocks, including one cut off before its END line |
| Assignments | `KEY=v`, `"key":"v"`, `key: v`, logfmt, Go maps, percent-encoded, JSON inside JSON; any key whose words say secret |
| URLs | `user:password@`, a token as the whole userinfo, secret and signature query parameters, `user:pass@tcp(` DSNs |
| Command lines | `--token x`, `--password=x`, `curl -u user:pass`, `sshpass -p`, `mysql -pPASS`, `aws configure set aws_secret_access_key v` |
| Known values | exact match, plus URL-, JSON-, HTML-, Go- and base64-encoded forms ([`Scope`](scope.go)) |
| Obfuscation | zero-width and format characters inside a key, fullwidth letters |

## Behavior worth knowing

- **One marker, idempotent.** `[REDACTED]` everywhere; redacting twice equals redacting once
  (fuzzed). Quotes are kept, so redacted JSON stays JSON.
- **Whole words, not substrings.** `tokenizer`, `author`, `keyboard`, `passport` are left
  alone. `max_tokens`, `PASSWORD_MIN_LENGTH`, `token_type`, `credential_missing`
  describe a secret without being one. `--password-file` and `--password-stdin` are left.
- **References are not secrets.** `op://`, `keychain://`, `vault://`, `file://`, ... name a
  secret held elsewhere. Add schemes with `WithReferenceSchemes`.
- **Prose is left alone.** "accepts a Bearer JWT only", "use Bearer auth", "set X-API-Key
  header". Text your own application wrote can be marked with `Guidance`, so the rules never
  eat a recovery instruction; only a wrapped cause is redacted.
- **Fail closed.** A custom `Rule` with a bad pattern is an error from `New`, never silently
  skipped. A `Scope` reports values too short to protect (`Unprotected()`, names only) instead
  of pretending to cover them.
- **Linear time.** Patterns are RE2 and the key scanners are single-pass; hostile inputs
  (1 MiB of repeated `Bearer`, `authorization:`, `password=`, unterminated PEM) finish in
  well under a second. `go test -bench Text` for throughput.

## Composing with the rest

- `core/panics` takes `Config.Redact`; pass `redact.Default().Text` (or your configured
  `Redactor`'s). Without one it withholds panic text entirely.
- `security/secrets` resolves credentials; feed them to a `Scope` as they resolve (`Add`), so
  the error text of the call that used them is covered.
- Environment scrubbing for child processes should use `redact.SensitiveName` so the
  scrubber and the redactor agree on what a secret name is.

## Limits

A safety net, not a guarantee.

- A secret with no label and no known shape (a bare base64 blob) cannot be told from data.
  Only a known value (`Scope`) protects it. `WithHeuristics()` adds the one common
  unlabeled shape (an AWS secret access key) and can match ordinary 40-character
  identifiers, so it is off by default.
- The credential-only `Redactor` does not infer personal data. Use `Sanitizer`
  for the deterministic PII rules described below.
- `Writer` redacts a stream line by line in bounded memory. A value longer than `WithHold` that straddles a forced flush of a very long unbroken line can be missed; `Tail` handles a single chunk that was cut in the middle of a known value.
- Redact at the boundary, and prefer not to put secrets in text at all.

## Conformance

`corpus_test.go` is the shared suite: about 100 invented cases (secrets that must go, text that
must stay, and the exact collateral that must survive). Every credential-shaped value is
assembled at run time so the source holds no complete fake credential. When an application
finds a leak or a false positive, add the case here first, then fix the rule. Known gaps are
listed in the test, with the reason.

`go test -fuzz=FuzzText ./security/redact` hunts for invariant violations.

### Event payload sanitization

`NewSanitizer(Policy, ...Option)` compiles an immutable per-kind policy.
`Sanitize(payload)` returns JSON bytes and a typed `Report`, without importing
an event envelope or transport. The event-stage adapter maps the report into
its envelope's class fields. Any nonempty `Report.ErrorClass` means the caller
must mark the envelope **unsanitized**, even though the returned tombstone is
safe to emit.

```go
s, err := redact.NewSanitizer(redact.Policy{
    AllowedPaths: []string{"/entity/type", "/entity/id", "/to", "/trigger/kind"},
    Sensitivity: redact.Private,
})
if err != nil { /* refuse to start with an invalid policy */ }
clean, report := s.Sanitize(payload)
```

- Zero mode is `Allowlist`. Only listed values survive; explicit `Denylist`
  keeps the shape and applies credential, key-name and PII rules. Producers
  select these policies per kind: existing catalog kinds use denylist, new
  kinds use their allowlist, and unknown kinds get an empty allowlist.
- Paths use RFC 6901 escaping (`~0`, `~1`); `*` matches an array index. Listing a
  container does not grant access to its descendants. List each retained leaf.
  Removed array elements become `null` so other indices remain stable.
- Mandatory PII detectors cover ASCII email addresses, international phone
  numbers (7–15 digits with `+`) and national numbers (10–11 digits), validated
  IPv4/IPv6, 13–19 digit Luhn-valid cards, and Nanite's 32-hex identifiers.
  PII also scans a single percent-decoded view, mapping replacements to the
  original encoded bytes. The 32-hex rule alone exempts `trace_id`, `span_id`,
  `traceparent`, `*_digest` and `*_hash` values; other rules still run.
  Formats are deterministic, not a guarantee to recognize every personal datum.
  Internationalized email addresses, names and inferred locale-specific PII
  are not modeled. Use an allowlist for untrusted/new event kinds.
- Secret rules and key matches raise sensitivity to `Secret`; PII raises it to
  at least `Private`. Policy classification can only raise it. Overlapping
  matches and dropped subtrees retain both classifications.
- Counts describe replaced spans or removed values; a whole dropped subtree
  counts once. Report paths never include values; PII/credential-bearing keys
  and all non-allowlisted key segments are replaced by `*` in report paths,
  including in denylist mode. PII-bearing key members are removed. Dropping a
  benign field does not raise sensitivity; inspection results do. Already absent values
  and null array placeholders do not add redactions on a second pass.
- The default size cap is 64 KiB (configurable), applied to input and output.
  Oversized, opaque, invalid JSON (including duplicate keys/invalid UTF-8), or
  nesting deeper than 64 levels produces `{"sanitize_error":"<class>"}`.
  Failure classification is `Secret`; no parse error text or input is returned.

The credential-only `Default()` behavior is unchanged. Sanitizer options
customize the underlying redactor but cannot disable its mandatory PII rules.
A runtime scope may still learn known values concurrently.

Run the seeded event corpus and fuzz checks with `go test ./...` and
`go test -run '^$' -fuzz '^FuzzSanitize$' -fuzztime=30s`. The benchmark
`go test -run '^$' -bench BenchmarkSanitize4KB -benchtime=10000x -cpu=1`
reports wall-clock per-call p99 over up to 10,000 samples, plus allocations and
mean latency. Its 4 KiB payload includes PII and a synthetic provider credential.
CPU profiles distinguish execution cost from scheduling; they do not establish
per-call CPU-time p99. The release target is p99 below 200 microseconds; measure
on a controlled host before claiming the gate passed.

Structured key checks strip Unicode format characters then apply NFKC, using
`golang.org/x/text/unicode/norm`. Joined secret-key words (such as `pass_word`)
also match; benign metadata suffixes retain their existing precedence.

On Linux, `go test -run '^$' -bench '^BenchmarkSanitize4KBCPU$'
-benchtime=10000x -cpu=1 -count=10` additionally reports `cpu-ns/op` using process
user+system CPU time (`getrusage`), including GC workers. Compare repeated runs
with `benchstat`. This is mean CPU per call, not a percentile; keep it distinct
from wall-clock p99. Returned JSON, reports and parse structures own their memory,
so the API still allocates; it does not pool unredacted payload buffers.

For per-call CPU percentiles, use `-bench '^BenchmarkSanitize4KBCPUSamples$'`
with `-benchtime=10000x -cpu=1`. It reports CPU median and p99 over 10,000
individual calls using process user+system CPU with microsecond resolution.
The sampling syscall overhead is included, not subtracted. These CPU samples
are distinct from the wall-clock samples reported by `BenchmarkSanitize4KB`.
