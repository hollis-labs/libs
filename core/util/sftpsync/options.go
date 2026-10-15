package sftpsync

import "os"

// SymlinkPolicy decides what a symlink inside the tree means.
//
// The default refuses to follow anything out of the sync root. The policy
// that lifts that refusal is named so that choosing it reads as a decision
// at the call site rather than as a tuning knob.
type SymlinkPolicy int

const (
	// SymlinkReplicate recreates a symlink on the destination with the same
	// target, and refuses one whose target escapes the sync root with
	// [ErrSymlinkEscape]. Nothing is read through the link, so a link to a
	// 40GB file or to a device node costs nothing and a loop is impossible.
	//
	// An absolute target counts as an escape even when it happens to point
	// inside the source root: the same absolute path on the other machine is
	// a different place, and this package will not guess which one you meant.
	//
	// This is the default.
	SymlinkReplicate SymlinkPolicy = iota

	// SymlinkSkip transfers no symlinks at all and records each one in the
	// [Result] as skipped. Nothing is read and nothing is created, so an
	// escaping link is not an error under this policy — it is simply not
	// copied. Use it when the destination must contain only regular files.
	SymlinkSkip

	// SymlinkDereferenceIncludingOutsideRoot follows every symlink and copies
	// what it points at, including targets outside the sync root. A link to
	// /etc/shadow copies /etc/shadow.
	//
	// This is the escape hatch, and the name is the warning. Choose it only
	// when you control the tree and know that following is what you want.
	// Symlink loops terminate at [WithMaxDepth] with [ErrMaxDepthExceeded]
	// rather than running forever.
	SymlinkDereferenceIncludingOutsideRoot
)

// String implements fmt.Stringer.
func (p SymlinkPolicy) String() string {
	switch p {
	case SymlinkReplicate:
		return "replicate"
	case SymlinkSkip:
		return "skip"
	case SymlinkDereferenceIncludingOutsideRoot:
		return "dereference-including-outside-root"
	default:
		return "unknown"
	}
}

// Option configures a transfer. Options are functional so that adding one
// does not break callers, and so that a call site names only what it changes.
type Option func(*config)

type config struct {
	preserveMode    bool
	preserveModTime bool
	symlinks        SymlinkPolicy
	dryRun          bool
	fileMode        os.FileMode
	dirMode         os.FileMode
	maxDepth        int
}

const (
	defaultFileMode os.FileMode = 0o644
	defaultDirMode  os.FileMode = 0o755
	defaultMaxDepth             = 64
)

func newConfig(opts []Option) config {
	cfg := config{
		preserveMode: true,
		symlinks:     SymlinkReplicate,
		fileMode:     defaultFileMode,
		dirMode:      defaultDirMode,
		maxDepth:     defaultMaxDepth,
	}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.maxDepth < 1 {
		cfg.maxDepth = defaultMaxDepth
	}
	return cfg
}

// WithPreserveMode carries the source permission bits to the destination.
// Default: true.
//
// It is on by default because the alternative surfaces an hour later: an
// uploaded script that arrives 0644 fails at the moment something tries to
// run it, far from the transfer that caused it. With it off, files get
// [WithFileMode] and directories [WithDirMode].
func WithPreserveMode(preserve bool) Option {
	return func(c *config) { c.preserveMode = preserve }
}

// WithPreserveModTime carries the source modification time to the
// destination. Default: false.
//
// Off by default because a transferred file is new at the destination and
// most callers want its mtime to say so. Turn it on when something
// downstream compares timestamps across the two sides.
func WithPreserveModTime(preserve bool) Option {
	return func(c *config) { c.preserveModTime = preserve }
}

// WithSymlinkPolicy chooses what symlinks inside the tree mean.
// Default: [SymlinkReplicate].
func WithSymlinkPolicy(p SymlinkPolicy) Option {
	return func(c *config) { c.symlinks = p }
}

// WithDryRun walks the source and reports what would happen without creating,
// writing, renaming or changing anything at the destination. Default: false.
//
// It is an option on the transfer functions rather than a separate Plan call
// on purpose: a preview is only worth showing an operator if it came from the
// same walk as the transfer, and a second entry point is a second thing to
// keep in step.
func WithDryRun(dry bool) Option {
	return func(c *config) { c.dryRun = dry }
}

// WithFileMode sets the permission bits given to transferred files when
// [WithPreserveMode] is off. Default: 0644. Ignored while preserving.
func WithFileMode(mode os.FileMode) Option {
	return func(c *config) { c.fileMode = mode.Perm() }
}

// WithDirMode sets the permission bits given to created directories when
// [WithPreserveMode] is off. Default: 0755. Ignored while preserving.
func WithDirMode(mode os.FileMode) Option {
	return func(c *config) { c.dirMode = mode.Perm() }
}

// WithMaxDepth bounds how far below the sync root the walk descends, counting
// the root as depth 0. Default: 64. A value below 1 restores the default.
//
// Exceeding it is [ErrMaxDepthExceeded] rather than a silent truncation: a
// tree deeper than the limit is a tree this package has not fully copied, and
// a caller must not be told otherwise.
func WithMaxDepth(depth int) Option {
	return func(c *config) { c.maxDepth = depth }
}
