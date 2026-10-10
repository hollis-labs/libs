package repositories

import (
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

func cloneRequest(requirement CloneRequirement) Request {
	return Request{Mode: Clone, Ownership: Owned, Base: effects.RootInput{PrivateCustody: true}, Branch: "agent/clone", BaseCommit: "0123456789abcdef0123456789abcdef01234567", Clone: CloneIntent{Requirement: requirement}}
}

func TestCloneShapes(t *testing.T) {
	required := cloneRequest(CloneRequired)
	if !cloneShape(required, true) || !frozen(required) {
		t.Fatal("frozen required clone refused")
	}
	withFallback := required
	withFallback.Clone.FallbackAuthorizationID, withFallback.Clone.FallbackAuthorizationVersion = "grant", "1"
	if cloneShape(withFallback, true) {
		t.Fatal("required clone accepted a fallback authorization")
	}
	preselected := required
	preselected.Clone.Method = Reflink
	if frozen(preselected) || cloneShape(preselected, true) {
		t.Fatal("caller-selected method accepted as a frozen request")
	}
	if !cloneShape(preselected, false) {
		t.Fatal("selected clone refused")
	}
	unauthorized := cloneRequest(ClonePreferred)
	unauthorized.Mode, unauthorized.Clone.Method, unauthorized.Clone.FallbackReason = Worktree, NoClone, "reason"
	if cloneShape(unauthorized, false) {
		t.Fatal("fallback without explicit authorization accepted")
	}
	unauthorized.Clone.FallbackAuthorizationID, unauthorized.Clone.FallbackAuthorizationVersion = "grant", "1"
	if !cloneShape(unauthorized, false) || frozen(unauthorized) {
		t.Fatal("authorized fallback shape")
	}
	for _, m := range []CloneMethod{NoClone, FixtureCopy, "plain_copy"} {
		if m.CopyOnWrite() {
			t.Fatal("non-COW method reported copy on write", m)
		}
	}
	existing := required
	existing.Existing = true
	if cloneShape(existing, true) {
		t.Fatal("existing clone attachment accepted")
	}
	plain := cloneRequest("")
	plain.Mode, plain.Clone = Worktree, CloneIntent{}
	if !cloneShape(plain, true) {
		t.Fatal("plain worktree refused")
	}
}

func TestBindSelectionRejectsForeignReceiptFields(t *testing.T) {
	r := cloneRequest(CloneRequired)
	a := effects.AttachmentEvidence{Mode: string(Clone), RequestedMode: string(Clone), Method: string(Reflink)}
	if bound, ok := BindSelection(r, a); !ok || bound.Clone.Method != Reflink {
		t.Fatal("clone receipt not bound", bound, ok)
	}
	for name, mutate := range map[string]func(*effects.AttachmentEvidence){
		"merge target":   func(a *effects.AttachmentEvidence) { a.TargetBranch = "main" },
		"fallback grant": func(a *effects.AttachmentEvidence) { a.FallbackAuthorizationID = "grant" },
		"required fallback": func(a *effects.AttachmentEvidence) {
			a.Mode, a.Method, a.FallbackReason = string(Worktree), string(NoClone), "reason"
		},
		"no request mode": func(a *effects.AttachmentEvidence) { a.RequestedMode = "" },
		"unknown method":  func(a *effects.AttachmentEvidence) { a.Method = "plain_copy" },
	} {
		forged := a
		mutate(&forged)
		if _, ok := BindSelection(r, forged); ok {
			t.Fatal("bound forged receipt", name)
		}
	}
	plain := cloneRequest("")
	plain.Mode, plain.Clone = Worktree, CloneIntent{}
	if _, ok := BindSelection(plain, effects.AttachmentEvidence{Mode: string(Worktree), Method: string(Reflink)}); ok {
		t.Fatal("worktree request bound a clone receipt")
	}
}
