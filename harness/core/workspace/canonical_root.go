package workspace

import "path/filepath"

// CanonicalRootRef returns ref with Path and AllowedBase rewritten to their
// physical (symlink-resolved) spelling, which is the only spelling InspectRoot
// accepts. Use it when a root is declared through a symlinked path, for
// example a workspace directory that is a link onto another volume.
//
// It never relaxes InspectRoot: the result is still checked there, and the
// root must stay inside its allowed base after resolution. Path may name a
// directory that does not exist yet; its nearest existing ancestor is
// resolved and the missing suffix is appended unchanged. AllowedBase must
// exist. ID, Owner and Provenance are copied as given.
func CanonicalRootRef(ref RootRef) (RootRef, error) {
	if !filepath.IsAbs(ref.Path) || !filepath.IsAbs(ref.AllowedBase) {
		return RootRef{}, refuse(CodeUnsafeRoot, "root", Conflict)
	}
	base, err := filepath.EvalSymlinks(filepath.Clean(ref.AllowedBase))
	if err != nil {
		return RootRef{}, refuse(CodeCanonicalBaseUnavailable, "roots", Unsupported)
	}
	path, err := canonicalMissing(filepath.Clean(ref.Path))
	if err != nil || !within(base, path) {
		return RootRef{}, refuse(CodeNoncanonicalRoot, "roots", Conflict)
	}
	out := ref
	out.Path, out.AllowedBase = path, base
	return out, nil
}
