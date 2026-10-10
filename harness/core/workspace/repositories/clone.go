package repositories

import (
	"context"
	"path/filepath"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// CloneMethod names the construction actually used for a clone attachment.
type CloneMethod string

const (
	// Reflink is a Linux FICLONE copy-on-write file clone.
	Reflink CloneMethod = "reflink"
	// Clonefile is a macOS clonefile copy-on-write file clone.
	Clonefile CloneMethod = "clonefile"
	// NoClone means no copy-on-write method is available for the pair.
	NoClone CloneMethod = "none"
	// FixtureCopy is a plain byte copy used only by disposable test fixtures.
	// It is never copy-on-write and no production adapter selects it; receipts
	// carrying it record a fixture construction, not COW support.
	FixtureCopy CloneMethod = "fixture_copy"
)

// CopyOnWrite reports whether m is a real copy-on-write method.
func (m CloneMethod) CopyOnWrite() bool { return m == Reflink || m == Clonefile }

type CloneRequirement string

const (
	// CloneRequired refuses with Unsupported before any mutation when no
	// copy-on-write method is available.
	CloneRequired CloneRequirement = "required"
	// ClonePreferred attaches a worktree instead only when the host supplied an
	// explicit fallback authorization; otherwise it is Unsupported.
	ClonePreferred CloneRequirement = "preferred"
)

// CloneIntent is the frozen clone request. Method and FallbackReason are set
// only by capability selection in this package; callers leave them empty.
// FallbackAuthorization names the host's explicit grant for the broader shared
// Git metadata access a worktree needs; it is never inferred from the clone
// grant.
type CloneIntent struct {
	Requirement                                           CloneRequirement
	FallbackAuthorizationID, FallbackAuthorizationVersion string
	Method                                                CloneMethod
	FallbackReason                                        string
}

func (c CloneIntent) fallbackAuthorized() bool {
	return c.Requirement == ClonePreferred && c.FallbackAuthorizationID != "" && c.FallbackAuthorizationVersion != ""
}

// ClonePort is the optional capability probe of a Port that can construct
// clone attachments. CloneCapability observes the actual source/destination
// filesystem pair and the source's clean base without mutating any user path;
// a disposable owned fixture may be created and removed beneath the private
// attachment base. It returns NoClone with a stable nonsecret reason when copy
// on write is unavailable. An error means the capability is unknown.
type ClonePort interface {
	CloneCapability(context.Context, Request) (CloneMethod, string, error)
}

// cloneShape reports whether the clone fields agree with the mode. frozen
// requests carry no selection; selected requests are produced here or bound
// from a receipt.
func cloneShape(r Request, frozen bool) bool {
	c := r.Clone
	if c == (CloneIntent{}) {
		return r.Mode != Clone
	}
	if c.Requirement != CloneRequired && c.Requirement != ClonePreferred {
		return false
	}
	if c.Requirement == CloneRequired && (c.FallbackAuthorizationID != "" || c.FallbackAuthorizationVersion != "") {
		return false
	}
	if (c.FallbackAuthorizationID == "") != (c.FallbackAuthorizationVersion == "") {
		return false
	}
	if r.Existing || r.Ownership != Owned || !r.Base.PrivateCustody || !ValidBranch(r.Branch) || !commit(r.BaseCommit) {
		return false
	}
	switch {
	case r.Mode == Clone && c.Method == "" && c.FallbackReason == "":
		return frozen
	case r.Mode == Clone && (c.Method.CopyOnWrite() || c.Method == FixtureCopy) && c.FallbackReason == "":
		return !frozen
	case r.Mode == Worktree && c.Method == NoClone && c.FallbackReason != "" && c.fallbackAuthorized():
		return !frozen
	}
	return false
}

// selectAttachment probes the pair and returns the request to construct. It
// never mutates: an unknown capability refuses, required COW without a method
// is Unsupported, and preferred COW falls back only with explicit authority.
func selectAttachment(ctx context.Context, r Request, p Port) (Request, effects.Outcome, string) {
	if r.Mode != Clone {
		return r, "", ""
	}
	method, reason := NoClone, "clone_port_unavailable"
	if cp, ok := p.(ClonePort); ok {
		m, why, e := cp.CloneCapability(ctx, r)
		if e != nil || ctx.Err() != nil {
			return r, effects.Refused, "clone_capability_unknown"
		}
		switch {
		case m.CopyOnWrite() || m == FixtureCopy:
			r.Clone.Method = m
			return r, "", ""
		case m == NoClone && why != "":
			method, reason = m, why
		default:
			return r, effects.Refused, "clone_capability_unknown"
		}
	}
	if r.Clone.fallbackAuthorized() {
		r.Mode = Worktree
		r.Clone.Method = method
		r.Clone.FallbackReason = reason
		return r, "", ""
	}
	return r, effects.Unsupported, "clone_unsupported"
}

// BindSelection rebinds a frozen request to the construction its attachment
// receipt records. Non-clone requests must carry no clone receipt fields; a
// fallback must name exactly the request's explicit fallback authorization.
// Callers still compare every other receipt field against the returned request.
func BindSelection(r Request, a effects.AttachmentEvidence) (Request, bool) {
	if a.TargetBranch != "" || a.TargetBefore != "" || r.Clone.Method != "" || r.Clone.FallbackReason != "" {
		return r, false
	}
	if r.Mode != Clone {
		return r, r.Clone == (CloneIntent{}) && a.RequestedMode == "" && a.Method == "" && a.FallbackReason == "" && a.FallbackAuthorizationID == "" && a.FallbackAuthorizationVersion == ""
	}
	if a.RequestedMode != string(Clone) {
		return r, false
	}
	r.Mode = Mode(a.Mode)
	r.Clone.Method = CloneMethod(a.Method)
	r.Clone.FallbackReason = a.FallbackReason
	if !cloneShape(r, false) {
		return r, false
	}
	if r.Mode == Worktree {
		return r, a.FallbackAuthorizationID == r.Clone.FallbackAuthorizationID && a.FallbackAuthorizationVersion == r.Clone.FallbackAuthorizationVersion
	}
	return r, a.FallbackAuthorizationID == "" && a.FallbackAuthorizationVersion == ""
}

// clonedGitDir is the private metadata directory of a constructed clone.
func clonedGitDir(r Request) string { return filepath.Join(r.Path, ".git") }
