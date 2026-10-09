// Package memstore is an in-memory [toolresult.Store]. It is safe for
// concurrent use, holds everything until process exit or DeleteExpired, and is
// intended for tests, single-process tools and short-lived sessions.
package memstore
