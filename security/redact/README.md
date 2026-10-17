# security/redact

Credential redaction, with opt-in PII detection, for text, JSON, command lines,
environments and logs. Uses the standard library and `golang.org/x/text` for
Unicode normalization. It is the one redactor to use where text leaves your control: logs,
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
- `Default()` is credential-only. Opt in with `WithPII` for deterministic personal
  data rules; `Sanitizer` always enables them. Neither infers personal names,
  free-form postal addresses, health information or other special categories.
  There is no ML or NER. Other national identifiers require explicit key policy
  or a future rule with its own validator and negative corpus.
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
- Mandatory PII uses exactly the same catalog, validators, normalization and
  key-name rules as `WithPII()` (see below). The 32-hex rule alone exempts
  `trace_id`, `span_id`, `traceparent`, `*_digest` and `*_hash` values;
  other rules still run. Use an allowlist for untrusted/new event kinds.
- Credential rules and credential key matches raise sensitivity to `Secret`; PII
  value and key matches raise it to
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
per-call CPU-time p99. The release target is CPU p99 below 200 microseconds,
or CPU median below 200 microseconds plus quiet-host wall p99 below 1 millisecond.
Measure on a controlled host before claiming the gate passed.

Structured key checks strip Unicode format characters then apply NFKC, using
`golang.org/x/text/unicode/norm`. Joined secret-key words (such as `pass_word`)
also match; benign metadata suffixes retain their existing precedence.
Whole-word key matching does not cover every Unicode spelling: for example,
`PASSWORDİ` (with a trailing non-ASCII letter) does not match `PASSWORD`.
Use explicit sensitive-key configuration for such names and allowlists for
untrusted event kinds. Value-level credential and PII inspection still applies.

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

## Opt-in PII for general text

```go
r, err := redact.New(redact.WithPII()) // all supported classes, including key rules
// To retain IPs while enabling every other class:
ops, err := redact.New(redact.WithPII(redact.PIIClasses(
    redact.PIIAll &^ redact.PIIIP,
)))
```

Use the configured instance's `Text`, `Bytes`, `Scan`, `JSON`, `Args`, `Env`,
`Handler`, `ErrorText`, `Writer` or `Tail`. `Guidance` still trusts application
prose and redacts only a wrapped cause. Package-level `Text`, `Default()` and
`SensitiveName` remain credential-only. `PIIName` is the separate predicate for
personal-data keys. Class constants can be combined with `|`; `PIIClasses()`
selects none, unknown bits fail `New`, and the last `WithPII` selection wins.
`WithoutRules` can suppress an individual text detector on a plain Redactor;
class selection governs key protection. `NewSanitizer` forces every PII class
and detector regardless of `WithPII`, `WithoutRules` or `WithBenignKeys`.

| Class | Value detection and validation |
|---|---|
| `PIIEmail` | ASCII and Unicode `local@domain` dot-atom subset. Unicode letters, numbers and combining marks; ASCII atom punctuation locally; dot-separated DNS-style domain labels. Rejects empty atoms, adjacent local dots, invalid label edges and excessive byte lengths (64 local, 63 per label, 254 total). No quoted local parts, address literals, DNS lookup or mailbox verification. |
| `PIIPhone` | Existing international `+` forms with 7–15 digits and national 10–11 digit forms; spaces, parentheses, dots and hyphens. Shape and boundaries only, no country allocation validation. Unlabeled 10-digit order numbers can be indistinguishable from phones; omit this class when those must survive. |
| `PIIIP` | IPv4 and IPv6 parsed by `net/netip`, including IPv6 zones. |
| `PIICard` | 13–19 digits with a nonzero Luhn-valid checksum, optionally separated by spaces or hyphens. No issuer verification. Adjacent digit groups are considered separately; an overlong contiguous number is not accepted by prefix. |
| `PIIIBAN` | Electronic or standard four-character space-grouped forms; uppercase country code, registered national length and BBAN character shape, check digits 02–98 and mod-97. Shapes are pinned to [SWIFT registry release 103](https://www.swift.com/swift-resource/9606/download), September 2026; no account existence or domestic bank/checksum verification. |
| `PIINationalID` | Hyphenated US SSN only: excludes area 000, 666 and 900–999, zero group and zero serial per [SSA](https://www.ssa.gov/employer/randomizationfaqs.html). UK NINO: two prefix letters, six digits and A–D suffix, optional spaces between pairs, case-insensitive; validates [HMRC prefix exclusions](https://www.gov.uk/hmrc-internal-manuals/national-insurance-manual/nim39110). Neither validates issuance. Bare nine-digit numbers and other national ID formats are not inferred. |
| `PIINaniteID` | Bounded 32-hex identifiers. Such values are indistinguishable from other 32-hex hashes; longer commit hashes and hyphenated UUIDs are not this shape. |
| `PIIDateOfBirth`, `PIIPersonalName`, `PIIPostalAddress` | **Key names only.** No bare-date, name or address detection in prose. |

PII keys include `email`, `email_address`, `phone`, `phone_number`, `ssn`, `nino`,
`national_id`, `iban`, `card_number`, `ip_address`, `dob`, `birth_date`,
`date_of_birth`, `first_name`, `last_name`, `full_name`, `given_name`, `surname`,
`address`, `postal_address`, `street_address` and `mailing_address`.
CamelCase, separators, Unicode normalization and prefixed keys are supported.
`email_count`, `phone_type`, `first_name_count` and `birth_date_format` stay
metadata keys; their string values are still scanned. Selected classes govern
both value detectors and key rules. Key matches remove a value without trying
to validate it, including whole objects in JSON and groups in slog; booleans,
nulls and existing placeholder/reference values keep the established behavior.
`WithBenignKeys` customizes credential policy, not opted-in PII policy.

PII scans strip Unicode format characters and apply NFKC, then also inspect one
percent-decoded view with the same normalization. Replacements use original
byte offsets. The credential-only text normalization stays unchanged. This is
a supported syntax subset, not full email internationalization/IDNA validation.
`Finding.Rule` contains a value-free rule name (`pii-email`, `pii-iban`,
`pii-ssn`, `pii-nino`, `pii-key-name`, etc.). Merged overlapping/touching spans
keep one representative PII rule in preference to a credential rule, so PII spans
can be counted without values. It is not a count of every detector match;
Sanitizer observes all raw matches for sensitivity classification.

`Writer` holds incomplete lines across writes and retains the key/quote context
of multiline values, including an unterminated quoted PII value at `Close`.
Its existing forced-flush/hold-window limits still apply. `Tail` runs all enabled
PII rules on complete values in the supplied tail; **unknown PII fragments cut
at the edges cannot be reconstructed**. Only Scope-known values get fragment
protection. Consumer adoption is a separate change after a maintainer release.

Run `go test ./...` for the credential and PII corpora, streaming and structured
paths. `python3 testdata/check_validators.py` replaces each new validator (plus
Luhn) with unconditional acceptance in a disposable copy and requires the public
detector's negative tests to fail. `go test -run '^$' -fuzz '^FuzzPIIText$'
-fuzztime=30s` checks PII idempotence. No fixture contains a complete new
PII-shaped literal: the corpus builds them at runtime with `j`.

### Measured cost

Measured on 2026-10-16 and 2026-10-17, Linux/amd64, Go 1.26.9, Intel
i5-11400F (6 cores / 12 logical CPUs). These are
measurements on a shared development host, not a universal latency guarantee.

For default-path comparison, the new benchmark file was backported to clean
base `a5fb9319` without enabling PII. Ten alternating before/after runs of
`go test -run '^$' -bench '^BenchmarkText4KBDefault$|^BenchmarkText4KBMixed$/default$'
-benchtime=15000x -cpu=1` gave these medians of run means:

| PII off, 4 KiB | Before | After | Allocations before/after |
|---|---:|---:|---:|
| Ordinary prose | 41.84 µs | 38.91 µs | 152 B, 4 allocs |
| Mixed text | 46.04 µs | 45.71 µs | 4,248 B, 5 allocs |

No default-path regression was observed in these samples; timing ranges overlap.
The credential-only scan does not construct PII views or run PII finders.

On the final implementation, three runs on 2026-10-17 measured:

| PII on, 4 KiB | Median of run means | Run range | Median allocations |
|---|---:|---:|---:|
| Ordinary prose | 45.46 µs | 44.84–46.33 µs | 160 B, 5 allocs |
| Mixed email/IBAN/SSN/NINO | 98.59 µs | 97.07–99.91 µs | 8,984 B, 17 allocs |

Sanitizer measured **CPU median 88–89 µs**, CPU p99 180–185 µs, and
**wall p99 165–176 µs**, at 253 allocations per call. These pass the requested
CPU-median <200 µs / quiet-host wall-p99 <1 ms gate. Each percentile is over
10,000 calls per run; the Text numbers above are medians of three run means,
not per-call percentiles.

The benchmark started after five consecutive one-second samples above 90% host
CPU idle, with no other test, compiler, linker or scanner jobs observed.
One-second samples throughout the run observed no such competing jobs and
84.2–92.3% host idle (median 87.7%), including the benchmark's own CPU use.
Background services remained running. The test binary was compiled beforehand
to exclude compilation from that observation window:

```sh
GOWORK=off go test -c -o /tmp/redact-pii-bench.test
/tmp/redact-pii-bench.test -test.run='^$' \
  -test.bench='^BenchmarkText4KB|^BenchmarkSanitize4KB(CPUSamples)?$' \
  -test.benchtime=10000x -test.cpu=1 -test.count=3
```

Host conditions matter: earlier competing-test runs reached CPU medians
213–236 µs and wall p99 968 µs; other loaded runs measured PII Text medians
117.59 µs (ordinary prose) and 227.91 µs (mixed PII). Those observations are
not quiet-host acceptance evidence. The passing run above establishes the gate
for this implementation and host; it does not rely on the v0.2.0 waiver.
