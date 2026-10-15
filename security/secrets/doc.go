// Package secrets resolves secret references to secret values without letting
// the values leak.
//
// A reference names a secret instead of containing it:
//
//	keychain://<namespace>/<key>[/<more>]   the OS credential store (alias keyring://)
//	helper://<name>/<rest>                  an external helper binary
//	file:///abs/path, file://~/path         a private credential file
//	op://<vault>/<item>/<field>             1Password, through the op CLI
//	env://<VAR>                             an environment variable (opt-in)
//
// The grammar is always <scheme>://<authority>[/<path>...]. A [Backend] gives
// a scheme its meaning, so a secrets service (Vault, Infisical, one of our own)
// can be added later as another Backend without changing the grammar.
//
// Rules the package holds to:
//
//   - A resolved value is a [Secret]. It prints as [redacted] through every
//     fmt verb, encoding/json, encoding.TextMarshaler and log/slog. The only
//     way out is [Secret.Reveal] or [Secret.Bytes].
//   - An error never contains a secret, a helper's stderr, or the text of a
//     backend's own error. [Error.Cause] keeps the cause reachable for
//     errors.Is and errors.As; it is never rendered.
//   - A failed lookup says why in a stable [Kind]: not found, denied,
//     unavailable, invalid, empty. [Error.Guidance] says what to do about it.
//   - The environment is not a silent fallback. It is read only through an
//     explicit env:// reference or an opt-in [WithEnvFallback], and the
//     [Resolved.Source] always says so.
//   - Every resolved value is offered to the configured [ValueSink] before the
//     caller sees it, so a redactor knows it by the time any error is written.
package secrets
