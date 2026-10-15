package sftpsync

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Direction records which way a transfer went.
type Direction string

const (
	// DirectionUpload is local to remote.
	DirectionUpload Direction = "upload"
	// DirectionDownload is remote to local.
	DirectionDownload Direction = "download"
)

// Action is what happened to one entry in the tree.
type Action string

const (
	// ActionFile is a regular file whose contents were transferred.
	ActionFile Action = "file"
	// ActionDir is a directory that exists at the destination and was walked.
	ActionDir Action = "dir"
	// ActionSymlink is a symlink recreated at the destination.
	ActionSymlink Action = "symlink"
	// ActionSkip is an entry deliberately not transferred. [Entry.Reason]
	// says why.
	ActionSkip Action = "skip"
)

// Entry is one thing the walk found. The set of entries is identical for a
// dry run and for the transfer it previews.
type Entry struct {
	// Path is relative to the sync root and always slash-separated, on both
	// sides and on every OS, so it is comparable between the two ends. The
	// root itself is ".".
	Path string
	// Action is what happened, or what would have happened under a dry run.
	Action Action
	// Mode is the permission bits the destination entry was given. It is zero
	// for [ActionSymlink], where nothing is chmodded — a link's own bits are
	// not portable and are not carried. For [ActionSkip] it is the source's,
	// for information.
	Mode os.FileMode
	// Size is the byte count, for regular files only.
	Size int64
	// Target is the symlink target as written, when Action is [ActionSymlink].
	Target string
	// Reason explains the skip, when Action is [ActionSkip]. Empty otherwise.
	Reason string
}

// Result is what happened. Transfers return one even when they fail, so a
// caller can tell an operator how far the transfer got before it stopped —
// which is the difference between "nothing moved" and "half the tree moved".
type Result struct {
	// Direction is which way the transfer went.
	Direction Direction
	// Source and Destination are the two roots, cleaned.
	Source      string
	Destination string
	// DryRun reports whether anything was actually written.
	DryRun bool
	// Entries is every entry the walk reached, in the order it reached them:
	// a directory before its contents, and names within a directory sorted,
	// so two runs over an unchanged tree report the same thing.
	Entries []Entry
	// Files, Dirs, Symlinks and Skipped count Entries by action.
	Files    int
	Dirs     int
	Symlinks int
	Skipped  int
	// Bytes is the total size of the files transferred.
	Bytes int64
	// Duration is how long the walk took, including the failed attempt when
	// the transfer returned an error.
	Duration time.Duration
}

// String is a one-line operator-facing summary.
func (r *Result) String() string {
	if r == nil {
		return "sftpsync: no result"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s -> %s", r.Direction, r.Source, r.Destination)
	if r.DryRun {
		b.WriteString(" (dry run)")
	}
	fmt.Fprintf(&b, ": %d files, %d dirs", r.Files, r.Dirs)
	if r.Symlinks > 0 {
		fmt.Fprintf(&b, ", %d symlinks", r.Symlinks)
	}
	if r.Skipped > 0 {
		fmt.Fprintf(&b, ", %d skipped", r.Skipped)
	}
	fmt.Fprintf(&b, ", %d bytes in %s", r.Bytes, r.Duration.Round(time.Millisecond))
	return b.String()
}

func (r *Result) record(e Entry) {
	r.Entries = append(r.Entries, e)
	switch e.Action {
	case ActionFile:
		r.Files++
	case ActionDir:
		r.Dirs++
	case ActionSymlink:
		r.Symlinks++
	case ActionSkip:
		r.Skipped++
	}
}
