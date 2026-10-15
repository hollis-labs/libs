package secrets

import (
	"fmt"
	"strings"
	"unicode"
)

// Ref is a parsed secret reference: <scheme>://<authority>[/<path>...].
//
// Raw is the reference as written and is safe to log: a reference names a
// secret, it never contains one.
type Ref struct {
	Raw       string
	Scheme    string
	Authority string
	Path      []string
}

// String returns the reference as written.
func (r Ref) String() string { return r.Raw }

// Key returns authority and path joined with "/", for example "openai/work".
func (r Ref) Key() string {
	if len(r.Path) == 0 {
		return r.Authority
	}
	return r.Authority + "/" + strings.Join(r.Path, "/")
}

// ParseRef parses a reference.
//
// The text of a failed parse never appears in the error: a caller that passes
// a literal secret by mistake must not get it echoed back.
//
// The file scheme is the one exception to the grammar. Its rest is a path
// (file:///abs/path or file://~/path) and is kept whole in Path[0].
func ParseRef(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, &Error{Kind: KindInvalid, Reason: "secret reference is empty"}
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || !validScheme(scheme) || rest == "" {
		return Ref{}, &Error{Kind: KindInvalid, Reason: "not a secret reference: expected <scheme>://<authority>/<path>"}
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return Ref{}, &Error{Kind: KindInvalid, Ref: scheme + "://…", Reason: "secret reference contains a control character"}
		}
	}
	if scheme == "file" {
		return Ref{Raw: raw, Scheme: scheme, Path: []string{rest}}, nil
	}
	authority, pathRaw, hasPath := strings.Cut(rest, "/")
	if authority == "" {
		return Ref{}, &Error{Kind: KindInvalid, Ref: scheme + "://…", Reason: "secret reference has no authority"}
	}
	ref := Ref{Raw: raw, Scheme: scheme, Authority: authority}
	if hasPath {
		ref.Path = strings.Split(pathRaw, "/")
		for _, seg := range ref.Path {
			if seg == "" {
				return Ref{}, &Error{Kind: KindInvalid, Ref: scheme + "://" + authority + "/…", Reason: "secret reference has an empty path segment"}
			}
		}
	}
	return ref, nil
}

func validScheme(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// BuiltinSchemes are the schemes this package understands out of the box. The
// env scheme is only served by a Resolver built with [WithEnv].
var BuiltinSchemes = []string{"keychain", "keyring", "helper", "file", "op", "env"}

// IsRef reports whether s looks like a reference to one of the [BuiltinSchemes].
// A value that merely becomes a reference after variable expansion is the
// caller's to treat as a literal; see the README.
func IsRef(s string) bool {
	return hasKnownScheme(strings.TrimSpace(s), BuiltinSchemes)
}

func hasKnownScheme(s string, schemes []string) bool {
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || rest == "" {
		return false
	}
	for _, k := range schemes {
		if scheme == k {
			return true
		}
	}
	return false
}

func (r Ref) require(authority, path bool) error {
	if authority && r.Authority == "" {
		return &Error{Kind: KindInvalid, Ref: r.Raw, Reason: "reference has no authority"}
	}
	if path && len(r.Path) == 0 {
		return &Error{Kind: KindInvalid, Ref: r.Raw, Reason: fmt.Sprintf("%s reference needs a path", r.Scheme)}
	}
	return nil
}
