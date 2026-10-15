package secrets

import (
	"context"
	"os"
	"regexp"
	"strings"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EnvBackend serves env://<VAR>. It is registered only by [WithEnv]. An unset
// or empty variable is not found. Source is always "env:<VAR>".
type EnvBackend struct {
	lookup func(string) (string, bool)
}

// NewEnvBackend returns an EnvBackend reading through lookup (os.LookupEnv if nil).
func NewEnvBackend(lookup func(string) (string, bool)) *EnvBackend {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &EnvBackend{lookup: lookup}
}

// Scheme implements Backend.
func (*EnvBackend) Scheme() string { return "env" }

// Resolve implements Backend.
func (b *EnvBackend) Resolve(_ context.Context, ref Ref) (Resolved, error) {
	if len(ref.Path) != 0 || !envName.MatchString(ref.Authority) {
		return Resolved{}, newErr(KindInvalid, ref, "", "env references look like env://VAR_NAME", nil)
	}
	source := "env:" + ref.Authority
	v, ok := b.lookup(ref.Authority)
	if !ok || strings.TrimSpace(v) == "" {
		return Resolved{}, newErr(KindNotFound, ref, source, "the variable is not set", nil)
	}
	return Resolved{Secret: FromString(v), Source: source}, nil
}
