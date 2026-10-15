// Package bootkey encodes an enrolled identity as a stable boot-directory
// component. Callers validate enrollment and persist the encoded key next to
// the exact identity; display labels are not inputs. The host supplies the base
// directory and joins the persisted key with "current".
//
// Encode is pure and one-way. Its version prefix is part of the persisted
// encoding. Reuse persisted keys across restarts; a future encoding version
// must not silently relocate an existing identity. The readable slug is
// cosmetic: the full SHA-256 digest covers the exact, unnormalized identity
// bytes, including case and whitespace. Arbitrarily long inputs are hashed;
// only the cosmetic slug is truncated, so output length is always bounded.
//
// This is an identity encoding, not the legacy launcher's safe-name
// passthrough or project/profile/session composition keying. Those helpers
// are deliberately not provided here. This package performs no filesystem
// operations and does not authorize publication, cleanup or process use.
package bootkey
