package secrets

import (
	"context"
	"strings"
)

// HelperBackend serves helper://<name>/<rest>.
//
// Protocol v0 (compatible with the Cerberus helper): the backend runs
//
//	<name> resolve keychain://<rest>
//
// and reads the value from stdout. Exit 0 is success, 2 not found, 3 denied,
// anything else unavailable. The child gets the process environment minus any
// variable whose value is a reference, and its stderr is discarded. The name
// may not contain a path separator or be "." or "..": it is looked up on PATH
// and the helper directories, never taken as a path.
type HelperBackend struct {
	exec    execConfig
	schemes func() []string
}

// Scheme implements Backend.
func (*HelperBackend) Scheme() string { return "helper" }

// Resolve implements Backend.
func (b *HelperBackend) Resolve(ctx context.Context, ref Ref) (Resolved, error) {
	if err := ref.require(true, true); err != nil {
		return Resolved{}, err
	}
	name := ref.Authority
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return Resolved{}, newErr(KindInvalid, ref, "", "the helper name must be a bare executable name", nil)
	}
	source := "helper:" + name
	path, ok := b.exec.find(name)
	if !ok {
		return Resolved{}, newErr(KindUnavailable, ref, source, "helper "+name+" was not found", nil)
	}
	return runHelper(ctx, b.exec, b.schemes(), ref, source, path, []string{"resolve", "keychain://" + strings.Join(append([]string{}, ref.Path...), "/")})
}
