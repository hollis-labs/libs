// Package guard decides whether a call to an LLM resource may proceed, and
// learns from how each call ended.
//
// It has four parts, usable separately or together through Guard:
//
//   - CircuitBreaker trips after consecutive failures of a resource and, after
//     a cooldown, admits exactly one probe call. Concurrent callers are refused
//     until the probe reports, and results from calls admitted before a state
//     change are ignored.
//   - Classify sorts an error into a Class: quota, auth, transient, request,
//     connection or canceled. HTTPError and ParseRetryAfter build one from an
//     HTTP response, including the provider's retry-after.
//   - Cooldown records, per Key (resource, account, model), when a key may next
//     be tried: until the provider's retry-after, or a capped exponential
//     Backoff. What is cooled down depends on the Class, so a malformed request
//     never cools down a healthy account and an auth failure cools down the
//     whole account.
//   - Guard combines them per Key: the cooldown answers "when may we retry",
//     an optional QuotaCheck answers "how much remains", the breaker answers
//     "is it healthy", and Decision{Allowed, RetryAfter, Reason} is the result.
//
// The package keeps state in memory only and never opens a database. Every
// type takes a Clock, so tests can move time.
package guard
