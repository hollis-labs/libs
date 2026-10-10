package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/sandbox"
)

func TestTargetPolicyNestedScopeAndDenyPrecedence(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "nested")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	access := sandbox.ResolvedAccessPolicy{ID: "access", FS: sandbox.ResolvedFilesystemAccess{
		Write:   []sandbox.ResolvedPath{{Path: root}},
		Deny:    []sandbox.ResolvedPath{{Path: filepath.Join(root, "denied")}},
		Protect: []sandbox.ResolvedPath{{Path: filepath.Join(root, "control")}},
	}}
	plan, err := DeriveTargets(access, TargetPolicy{Version: "v1", Roots: []RootBinding{{"parent", root, "host"}, {"child", child, "host"}}, OptOut: []string{filepath.Join(root, "ignored")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.roots) != 2 || plan.Digest() == "" {
		t.Fatal("missing independent root plan")
	}
	parent := plan.roots[0]
	for _, rel := range []string{"nested/a.txt", "denied/a.txt", "control/db", "ignored/a.txt"} {
		if parent.allows(filepath.Join(root, rel)) {
			t.Fatalf("excluded scope accepted: %s", rel)
		}
	}
	if !parent.allows(filepath.Join(root, "source.go")) || !plan.roots[1].allows(filepath.Join(child, "source.go")) {
		t.Fatal("eligible content lost")
	}
	// A returned target must not provide mutable access to the private plan.
	targets := plan.Targets()
	targets[0].IncludePaths[0] = "denied"
	if !parent.allows(filepath.Join(root, "source.go")) {
		t.Fatal("caller changed plan")
	}
}

func TestTargetPolicyRequestsNeverBecomeGrants(t *testing.T) {
	root := t.TempDir()
	granted := filepath.Join(root, "granted")
	if err := os.Mkdir(granted, 0700); err != nil {
		t.Fatal(err)
	}
	access := sandbox.ResolvedAccessPolicy{ID: "a", FS: sandbox.ResolvedFilesystemAccess{Write: []sandbox.ResolvedPath{{Path: granted}}}}
	for _, requested := range []string{root, filepath.Join(root, "outside")} {
		_, err := DeriveTargets(access, TargetPolicy{Version: "v1", Roots: []RootBinding{{"root", root, "host"}}, OptIn: []string{requested}})
		if !errors.Is(err, ErrCoverageUnsupported) {
			t.Fatal("ungranted requested directory accepted")
		}
	}
	access.Legacy.DefaultAllow = true
	if _, err := DeriveTargets(access, TargetPolicy{Version: "v1", Roots: []RootBinding{{"root", root, "host"}}}); !errors.Is(err, ErrCoverageUnsupported) {
		t.Fatal("ambient default allow accepted")
	}
}

func TestTargetPolicyEmptyAndSymlinkCoverageRefuses(t *testing.T) {
	root := t.TempDir()
	access := sandbox.ResolvedAccessPolicy{ID: "a", FS: sandbox.ResolvedFilesystemAccess{Write: []sandbox.ResolvedPath{{Path: root}}}}
	policy := TargetPolicy{Version: "v1", Roots: []RootBinding{{"root", root, "host"}}, OptOut: []string{root}}
	if _, err := DeriveTargets(access, policy); !errors.Is(err, ErrCoverageUnsupported) {
		t.Fatal("empty effective coverage accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	policy.Roots[0].Root = alias
	policy.OptOut = nil
	if _, err := DeriveTargets(access, policy); !errors.Is(err, ErrCoverageUnsupported) {
		t.Fatal("symlink root accepted")
	}
}

func TestTargetPolicyDefaultPrivateRootsExcludedUnderParentGrant(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	access := sandbox.ResolvedAccessPolicy{ID: "a", Roots: sandbox.ResolvedRoots{Scratch: scratch}, FS: sandbox.ResolvedFilesystemAccess{Write: []sandbox.ResolvedPath{{Path: root}}}}
	policy := TargetPolicy{Version: "v1", Roots: []RootBinding{{"root", root, "host"}}}
	plan, err := DeriveTargets(access, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.roots[0].allows(filepath.Join(scratch, "artifact")) {
		t.Fatal("scratch captured by parent grant")
	}
	policy.OptIn = []string{scratch}
	plan, err = DeriveTargets(access, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.roots[0].allows(filepath.Join(scratch, "artifact")) {
		t.Fatal("explicit nonsensitive scratch opt-in lost")
	}
}

func TestTargetPolicyEqualRootsRetainLogicalProvenance(t *testing.T) {
	root := t.TempDir()
	access := sandbox.ResolvedAccessPolicy{ID: "a", FS: sandbox.ResolvedFilesystemAccess{Write: []sandbox.ResolvedPath{{Path: root}}}}
	policy := TargetPolicy{Version: "v1", Roots: []RootBinding{{"second", root, "second-host"}, {"first", root, "first-host"}}}
	plan, err := DeriveTargets(access, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets()) != 1 || len(plan.Bindings()) != 2 {
		t.Fatal("aliases duplicated capture or lost provenance")
	}
	firstDigest := plan.Digest()
	policy.Roots[0], policy.Roots[1] = policy.Roots[1], policy.Roots[0]
	plan, err = DeriveTargets(access, policy)
	if err != nil || plan.Digest() != firstDigest {
		t.Fatal("input ordering changed scope binding")
	}
}
