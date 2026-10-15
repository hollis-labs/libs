package pathgrants

import "context"

// Checker is the minimal lookup contract a file or shell tool needs to
// consult session-scoped grants without importing the *PathGrants type.
// PathGrants satisfies it directly.
//
// Threading the lookup through ctx (rather than a global) keeps tests
// hermetic and lets the tool transport stay a value type.
//
// LookupPath returns the same boolean as IsPathAllowed plus the match
// kind ("literal", "ancestor", "ancestor_session", "none") and, when the
// hit was via a parent-session lineage walk, the matching ancestor's
// session ID (empty otherwise). BucketSize reports how many grants are
// registered for the session itself (own bucket only — ancestor buckets
// are not counted), used to distinguish "no bucket" misses from "bucket
// exists but no match" misses in diagnostic surfaces.
type Checker interface {
	IsPathAllowed(sessionID, candidate string) bool
	LookupPath(sessionID, candidate string) (bool, LookupKind, string)
	BucketSize(sessionID string) int
	BestSessionDir(sessionID string) string
}

// pathGrantCtxKey carries the (sessionID, checker) pair so a tool
// can run a session-scoped grant check before falling through to a
// path-error result.
type pathGrantCtxKey struct{}

// pathGrantCtxValue is the value stored on the context.
type pathGrantCtxValue struct {
	sessionID string
	checker   Checker
}

// WithPathGrants returns a new context carrying the (sessionID,
// checker) pair. A nil checker or empty sessionID returns ctx unchanged
// so callers may pass through unconditionally.
func WithPathGrants(ctx context.Context, sessionID string, checker Checker) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionID == "" || checker == nil {
		return ctx
	}
	return context.WithValue(ctx, pathGrantCtxKey{}, pathGrantCtxValue{sessionID, checker})
}

// FromContext returns the (sessionID, checker) pair stamped
// by WithPathGrants, or ("", nil) if none was stamped. A tool
// reads this when its own allow-list rejects the user-supplied path; if the checker accepts it, the call proceeds.
func FromContext(ctx context.Context) (sessionID string, checker Checker) {
	if ctx == nil {
		return "", nil
	}
	v, _ := ctx.Value(pathGrantCtxKey{}).(pathGrantCtxValue)
	return v.sessionID, v.checker
}
