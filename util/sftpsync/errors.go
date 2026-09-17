package sftpsync

import (
	"errors"
	"fmt"
)

// Side names which end of a transfer a path lives on. Every error this
// package returns carries one. "permission denied" without a side sends an
// operator to the wrong machine, which is the failure mode this package
// exists to avoid.
type Side string

const (
	// Local is the machine running this code.
	Local Side = "local"
	// Remote is the machine on the far end of the SFTP connection.
	Remote Side = "remote"
)

var (
	// ErrSymlinkEscape reports a symlink whose target resolves outside the
	// sync root. It is a sentinel rather than a plain error because a caller
	// acts on it differently from an I/O failure: an escape is a property of
	// the tree that retrying will not fix, and is worth naming to an operator.
	//
	// Wrapped in a [*PathError] that names the link, so the offending path is
	// recoverable as well as the condition.
	ErrSymlinkEscape = errors.New("symlink target escapes the sync root")

	// ErrNotDirectory reports that a sync root is not a directory. This
	// package transfers trees; for a single file, pkg/sftp is enough on its own.
	ErrNotDirectory = errors.New("not a directory")

	// ErrMaxDepthExceeded reports that the walk descended past the configured
	// limit (see [WithMaxDepth]). Under [SymlinkDereferenceIncludingOutsideRoot]
	// this is also how a symlink loop terminates.
	ErrMaxDepthExceeded = errors.New("maximum directory depth exceeded")
)

// PathError is the error every operation in this package returns. It names
// the operation, the side it happened on and the path it happened to, and
// unwraps to the underlying cause — so errors.Is against [ErrSymlinkEscape],
// context.Canceled or fs.ErrPermission all work through it.
type PathError struct {
	// Op is the operation that failed: "open", "create", "mkdir", "readdir",
	// "lstat", "stat", "copy", "close", "chmod", "chtimes", "symlink",
	// "rename" or "walk".
	Op string
	// Side is the machine Path lives on.
	Side Side
	// Path is the absolute path on Side, as this package addressed it.
	Path string
	// Err is the underlying cause.
	Err error
}

func (e *PathError) Error() string {
	return fmt.Sprintf("sftpsync: %s %s %s: %v", e.Side, e.Op, e.Path, e.Err)
}

// Unwrap returns the underlying cause.
func (e *PathError) Unwrap() error { return e.Err }

func pathErr(op string, side Side, path string, err error) *PathError {
	return &PathError{Op: op, Side: side, Path: path, Err: err}
}
