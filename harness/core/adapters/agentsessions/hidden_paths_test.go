package agentsessions

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/harness/core/sandbox"
)

// CW-20261001-0263: StartOptions.HiddenPaths keeps a host's control-plane
// state unreadable by the agent it launches.

func hideSupported(t *testing.T) {
	t.Helper()
	caps := sandbox.ResolveBackendCapabilities("", sandbox.BackendAuto)
	if !caps.Supported || !slices.Contains(caps.Capabilities, sandbox.CapHidePaths) {
		t.Skipf("the %s backend on %s cannot hide paths (see TestHiddenPaths_UnsupportedPlatformFailsClosed)", caps.Backend, caps.GOOS)
	}
}

func TestHiddenPaths_OffIsANoOp(t *testing.T) {
	out, err := applyHiddenPaths(StartOptions{Workdir: t.TempDir()})
	if err != nil || out.Profile.ID != "" || out.SandboxPolicy != nil {
		t.Fatalf("no hidden paths must not install a sandbox: %+v, %v", out, err)
	}
}

func TestHiddenPaths_RejectsRelativePaths(t *testing.T) {
	if _, err := applyHiddenPaths(StartOptions{HiddenPaths: []string{"secrets"}}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative hidden path: err = %v, want a refusal", err)
	}
}

func TestHiddenPaths_ProviderRuntimeRejects(t *testing.T) {
	_, err := normalizeProviderStartOptions(StartOptions{HiddenPaths: []string{"/var/lib/app"}})
	if !errors.Is(err, ErrHiddenPathsUnsupported) {
		t.Fatalf("provider runtime: err = %v, want ErrHiddenPathsUnsupported", err)
	}
}

func TestHiddenPaths_UnsupportedPlatformFailsClosed(t *testing.T) {
	caps := sandbox.ResolveBackendCapabilities("", sandbox.BackendAuto)
	if caps.Supported && slices.Contains(caps.Capabilities, sandbox.CapHidePaths) {
		t.Skipf("%s enforces it", runtime.GOOS)
	}
	if _, err := applyHiddenPaths(StartOptions{HiddenPaths: []string{"/var/lib/app"}}); !errors.Is(err, ErrHiddenPathsUnsupported) {
		t.Fatalf("err = %v, want ErrHiddenPathsUnsupported (never run with the control plane readable)", err)
	}
}

func TestHiddenPaths_MergeInto(t *testing.T) {
	hideSupported(t)
	dir := t.TempDir()

	bare, err := applyHiddenPaths(StartOptions{HiddenPaths: []string{dir}})
	if err != nil || !bare.Profile.HostFilesystem || !slices.Equal(bare.Profile.FS.Hide, []string{dir}) {
		t.Fatalf("bare: %+v, %v; want the minimal host-filesystem profile hiding the path", bare.Profile, err)
	}

	// Protect then hide land on the same profile and are idempotent.
	both, err := applyProtectedPaths(StartOptions{ProtectedPaths: []string{dir}, HiddenPaths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	both, err = applyHiddenPaths(both)
	if err != nil {
		t.Fatal(err)
	}
	again, err := applyHiddenPaths(both)
	if err != nil || !slices.Equal(again.Profile.FS.Hide, []string{dir}) || !slices.Equal(again.Profile.FS.Protect, []string{dir}) {
		t.Fatalf("protect+hide: %+v, %v", again.Profile, err)
	}

	// A caller's own profile is copied, not mutated.
	own := sandbox.Profile{ID: "own", HostFilesystem: true}
	out, err := applyHiddenPaths(StartOptions{Profile: own, HiddenPaths: []string{dir}})
	if err != nil || !slices.Equal(out.Profile.FS.Hide, []string{dir}) || len(own.FS.Hide) != 0 {
		t.Fatalf("own profile: %+v, %v (original %+v)", out.Profile, err, own)
	}

	// A resolved policy gets a deny.
	resolved := sandbox.ResolvedAccessPolicy{ID: "r", Mode: sandbox.ConfinementRequired}
	viaPolicy, err := applyHiddenPaths(StartOptions{SandboxPolicy: &resolved, HiddenPaths: []string{dir}})
	if err != nil || len(viaPolicy.SandboxPolicy.FS.Deny) != 1 || len(resolved.FS.Deny) != 0 {
		t.Fatalf("policy: %+v, %v (original %+v)", viaPolicy.SandboxPolicy, err, resolved)
	}
}
