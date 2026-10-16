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
- It does not detect personal data (email, phone, card, address). `Rule` is the extension
  point; nothing is built in.
- `Writer` redacts a stream line by line in bounded memory. A value longer than `WithHold` that straddles a forced flush of a very long unbroken line can be missed; `Tail` handles a single chunk that was cut in the middle of a known value.
- Redact at the boundary, and prefer not to put secrets in text at all.

## Conformance

`corpus_test.go` is the shared suite: about 100 invented cases (secrets that must go, text that
must stay, and the exact collateral that must survive). Every credential-shaped value is
assembled at run time so the source holds no complete fake credential. When an application
finds a leak or a false positive, add the case here first, then fix the rule. Known gaps are
listed in the test, with the reason.

`go test -fuzz=FuzzText ./security/redact` hunts for invariant violations.
