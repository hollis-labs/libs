//go:build linux

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// CW-20261001-0263: Profile.FS.Hide in a host-filesystem profile.

func hostProfile(fs FSSpec) Profile {
	return Profile{ID: "host", HostFilesystem: true, Net: true, Subprocess: true, FS: fs}
}

func TestBwrapHideHostFilesystemArgs(t *testing.T) {
	ws := realDir(t)
	root := realDir(t)
	secrets := filepath.Join(root, "secrets")
	token := filepath.Join(root, "run", "operator.token")
	nested := filepath.Join(secrets, "inner")
	for _, dir := range []string{secrets, nested, filepath.Dir(token)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(token, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "not-there")
	args, err := BuildBwrapArgs(hostProfile(FSSpec{Hide: []string{token, nested, secrets, missing}}), ws)
	if err != nil {
		t.Fatalf("BuildBwrapArgs: %v", err)
	}
	if !containsSeq(args, []string{"--tmpfs", secrets}) {
		t.Errorf("directory not hidden by tmpfs: %v", args)
	}
	if !containsSeq(args, []string{"--ro-bind", "/dev/null", token}) {
		t.Errorf("file not hidden by a /dev/null bind: %v", args)
	}
	if slices.Contains(args, nested) || slices.Contains(args, missing) {
		t.Errorf("a path under a hidden directory, or a missing one, was mounted: %v", args)
	}
	if !slices.Contains(args, "--unshare-user") {
		t.Errorf("hiding needs the user namespace required, not tried: %v", args)
	}
}

func TestBwrapHideOrderedAfterProtect(t *testing.T) {
	ws := realDir(t)
	state := realDir(t)
	args, err := BuildBwrapArgs(hostProfile(FSSpec{Protect: []string{state}, Hide: []string{state}}), ws)
	if err != nil {
		t.Fatalf("BuildBwrapArgs: %v", err)
	}
	ro, tmp := argIndex(args, "--ro-bind", state), slices.Index(args, "--tmpfs")
	if ro < 0 || tmp < 0 || tmp < ro {
		t.Errorf("want the tmpfs after the read-only bind so it covers it: %v", args)
	}
}

func TestBwrapHideRefusals(t *testing.T) {
	ws := realDir(t)
	if _, err := BuildBwrapArgs(hostProfile(FSSpec{Hide: []string{"relative/path"}}), ws); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Errorf("relative hide path: err = %v, want ErrUnsupportedPolicy", err)
	}
	if _, err := BuildBwrapArgs(hostProfile(FSSpec{Hide: []string{"/"}}), ws); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Errorf("hiding /: err = %v, want ErrUnsupportedPolicy", err)
	}
	// Outside a host-filesystem profile Hide is refused, not ignored.
	_, err := BuildBwrapArgs(Profile{ID: "narrow", FS: FSSpec{Hide: []string{ws}}}, ws)
	if !errors.Is(err, ErrUnsupportedPolicy) || !strings.Contains(err.Error(), "FS.Deny") {
		t.Errorf("hide in a narrowed profile: err = %v, want ErrUnsupportedPolicy pointing at FS.Deny", err)
	}
}

func TestBwrapHideRefusesSymlinkTheChildCouldRepoint(t *testing.T) {
	ws := realDir(t)
	dir := realDir(t)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	protectDirWritable = func(string) bool { return true }
	defer func() { protectDirWritable = uidCanWrite }()
	if _, err := BuildBwrapArgs(hostProfile(FSSpec{Hide: []string{link}}), ws); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Errorf("hide through a re-pointable symlink: err = %v, want ErrUnsupportedPolicy", err)
	}
}

func TestHostHideCapabilityAndResolvedPolicy(t *testing.T) {
	caps := ResolveBackendCapabilities("linux", BackendLinuxBwrap)
	if !slices.Contains(caps.Capabilities, CapHidePaths) {
		t.Errorf("linux bwrap does not report %s: %v", CapHidePaths, caps.Capabilities)
	}
	dir := realDir(t)
	missing := filepath.Join(dir, "gone")
	out, err := ResolvedAccessPolicy{}.WithHidden(dir, missing, dir)
	if err != nil {
		t.Fatalf("WithHidden: %v", err)
	}
	if len(out.FS.Deny) != 1 || out.FS.Deny[0].Path != dir {
		t.Errorf("Deny = %+v, want the one existing path, once", out.FS.Deny)
	}
	if _, err := (ResolvedAccessPolicy{}).WithHidden("rel"); err == nil {
		t.Error("WithHidden accepted a relative path")
	}
}

// The real thing: a sandboxed shell cannot read what is hidden, the parent can,
// and the rest of the host stays visible. Skipped without a usable bwrap.
func TestApplyHideHostFilesystemReal(t *testing.T) {
	requireBwrapLinux(t)
	ws := realDir(t)
	root := realDir(t)
	state := filepath.Join(root, "state")
	run := filepath.Join(root, "run")
	for _, dir := range []string{state, run} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(state, "db"):           "state-secret",
		filepath.Join(run, "operator.token"): "token-secret",
		filepath.Join(run, "tetherd.sock"):   "stays",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `
[ -z "$(ls -A "$STATE" 2>/dev/null)" ] || exit 10
cat "$STATE/db" >/dev/null 2>&1 && exit 11
[ -z "$(cat "$TOKEN" 2>/dev/null)" ] || exit 12
[ "$(cat "$RUN/tetherd.sock")" = stays ] || exit 13
echo x > "$STATE/new" 2>/dev/null; [ ! -e "$STATE/new" ] || [ -d "$STATE" ] || exit 14
exit 0`
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "STATE="+state, "RUN="+run, "TOKEN="+filepath.Join(run, "operator.token"))
	profile := hostProfile(FSSpec{Protect: []string{state, run}, Hide: []string{state, filepath.Join(run, "operator.token")}})
	cleanup, err := Apply(cmd, profile, ws)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	defer cleanup()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandboxed script: %v\n%s", err, out)
	}
	for path, body := range files {
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Errorf("%s = %q, %v after the run; the parent must still see it unchanged", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "new")); err == nil {
		t.Error("a write into the hidden tmpfs reached the host")
	}
}
