// Package pathsafe provides bounded path resolution that prevents traversal
// outside a caller-specified root directory.
//
// The canonical entry point is ResolveUnder. It cleans the user-supplied
// path, joins it under the root, resolves symlinks where possible, and
// refuses the result if it escapes the root — including via symlinks or
// ".." components.
//
// A symlink is followed even when it is the last component and dangles: a link
// whose target does not exist yet is judged by where it points, because a caller
// that creates the returned path writes through the link. Relative link targets
// resolve against the link's real directory, and a chain of links is followed up
// to a fixed depth.
//
// Callers that need to classify escape errors can use errors.As to unwrap an
// *EscapeError.
//
// # Long-lived roots
//
// ResolveUnder re-resolves the root's symlinks on every call; it does not pin
// the root's identity. A caller that holds a long-lived root and must detect
// the root (or its parents) being swapped underneath it can use a known
// pattern: capture the root's os.FileInfo once at start-up and re-check it
// with os.SameFile before each operation, and Lstat each path component to
// reject symlinks. The github.com/hollis-labs/go-workflow-host/artifactfs
// package does this (see its Store.validateRoots and
// rejectArtifactSymlinkComponents). This package deliberately does not
// provide that as API; it is documented here as a pattern only.
package pathsafe
