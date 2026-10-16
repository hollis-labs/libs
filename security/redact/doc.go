// Package redact removes credential material from text before it is logged,
// stored, shown to an operator or sent to a model.
//
// # Two kinds of protection, composed
//
// A [Scope] holds the secrets you KNOW: values an application has just resolved.
// Matching them exactly (and in their URL-, JSON-, HTML- and base64-encoded forms)
// is precise. The pattern rules protect the secrets you do not know about: provider
// key shapes, Authorization headers, private keys, key=value pairs with a secret-named
// key, URLs with credentials, cookies, command-line flags. A [Redactor] applies both.
//
// # Choices that came from getting it wrong
//
//   - One marker, [Marker], everywhere; redacting twice equals redacting once.
//   - Quotes are kept, so redacted JSON is still JSON; [Redactor.JSON] is the right
//     tool for JSON and keeps key order and numbers.
//   - The scheme survives: "Authorization: Bearer [REDACTED]".
//   - A word is only a key when it is a whole word of the name: tokenizer, author and
//     keyboard are left alone; max_tokens and PASSWORD_MIN_LENGTH describe a secret
//     without being one. [SensitiveName] is that one predicate, for environment
//     scrubbing as well as redaction.
//   - A value that names a secret held elsewhere (op://, keychain://) is not a secret.
//   - Prose is left alone: "accepts a Bearer JWT only" and "set X-API-Key header".
//   - Text the application wrote itself (a recovery instruction) can be marked with
//     [Guidance] so the rules never eat it; only a cause is redacted.
//   - A bad custom pattern is an error from [New], never silently skipped.
//   - Format and zero-width characters, and fullwidth letters, are looked through, so
//     they are not a way to hide a key; what is replaced is the original text.
//
// # What it does not do
//
// It is a safety net, not a guarantee. It cannot find a secret that looks like
// ordinary text and has no label (a bare base64 blob); [WithHeuristics] adds the
// one such shape that is common. [Sanitizer] adds deterministic PII detection,
// per-kind field allowlists, classification and fail-closed payload limits.
// [WithPII] enables the same deterministic rules on any Redactor, optionally
// restricted by [PIIClasses]. [PIIName] identifies personal-data keys without
// changing [SensitiveName]. Names, addresses and birth dates are key-only;
// there is no inference of names, addresses or special categories from prose.
// Redact where text leaves your control, and prefer not to log secrets at all.
package redact
