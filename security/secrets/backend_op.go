package secrets

import "context"

// OpBackend serves op://<vault>/<item>/<field> by running the 1Password CLI:
//
//	op read --no-newline op://...
//
// The CLI handles authentication (including biometric unlock through the
// desktop app). Its stderr is discarded, so a failure cannot be classified
// more finely than unavailable; a missing item and a locked vault look alike.
// The SDK can replace this backend later through [WithBackend].
type OpBackend struct {
	exec    execConfig
	schemes func() []string
}

// Scheme implements Backend.
func (*OpBackend) Scheme() string { return "op" }

// Resolve implements Backend.
func (b *OpBackend) Resolve(ctx context.Context, ref Ref) (Resolved, error) {
	if err := ref.require(true, true); err != nil {
		return Resolved{}, err
	}
	path, ok := b.exec.find("op")
	if !ok {
		return Resolved{}, newErr(KindUnavailable, ref, "op", "the 1Password CLI (op) was not found", nil)
	}
	return runHelper(ctx, b.exec, b.schemes(), ref, "op", path, []string{"read", "--no-newline", ref.Raw})
}
