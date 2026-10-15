package secrets_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	secrets "github.com/hollis-labs/go-secrets"
	"github.com/hollis-labs/go-secrets/secretstest"
)

const fileValue = "file-store-value-0123456789"

func fileResolver(t *testing.T) (*secrets.Resolver, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state", "hollis", "secrets")
	return secrets.New(secrets.WithStateDir(dir), secrets.WithStore(&secretstest.FakeStore{})), dir
}

func TestFileStoreSetResolveRoundTrip(t *testing.T) {
	r, dir := fileResolver(t)
	ctx := context.Background()
	if err := r.Set(ctx, "file://openai/work", strings.NewReader(fileValue+"\n")); err != nil {
		t.Fatal(err)
	}
	res, err := r.Resolve(ctx, "file://openai/work")
	if err != nil {
		t.Fatal(err)
	}
	if res.Secret.Reveal() != fileValue || res.Source != "file" {
		t.Fatalf("got source %q", res.Source)
	}
	secretstest.AssertNoLeak(t, fileValue, res, res.Secret)

	if runtimeIsUnix() {
		for _, p := range []string{filepath.Join(dir, "openai"), dir} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("%s: want 0700, got %v (%v)", p, fi, err)
			}
		}
		fi, err := os.Stat(filepath.Join(dir, "openai", "work"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("entry: want 0600, got %v (%v)", fi, err)
		}
	}
}

func TestFileStoreSetOverwritesAtomicallyAndLeavesNoTemp(t *testing.T) {
	r, dir := fileResolver(t)
	ctx := context.Background()
	for _, v := range []string{"first-value-aaaa", "second-value-bbbb"} {
		if err := r.Set(ctx, "file://a/b", strings.NewReader(v)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := r.Resolve(ctx, "file://a/b")
	if err != nil || res.Secret.Reveal() != "second-value-bbbb" {
		t.Fatalf("resolve: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "a"))
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the secret", len(entries))
	}
}

func TestFileStoreRejectsTraversalAndBadSegments(t *testing.T) {
	r, dir := fileResolver(t)
	for _, ref := range []string{"file://a/../x", "file://a/.hidden", "file://onlyone", "file://a//b", "file://a/b c", "file://a/b/.."} {
		err := r.Set(context.Background(), ref, strings.NewReader("value-value-1"))
		if secrets.KindOf(err) != secrets.KindInvalid {
			t.Errorf("%s: kind %q, want invalid", ref, secrets.KindOf(err))
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x")); err == nil {
		t.Fatal("a traversal wrote outside the store")
	}
}

func TestFileStoreRefusesEmptyValueAndPathForm(t *testing.T) {
	r, _ := fileResolver(t)
	ctx := context.Background()
	if err := r.Set(ctx, "file://a/b", strings.NewReader("  \n")); secrets.KindOf(err) != secrets.KindEmpty {
		t.Errorf("empty: %v", err)
	}
	if err := r.Set(ctx, "file:///tmp/whatever", strings.NewReader("value-value-1")); secrets.KindOf(err) != secrets.KindInvalid {
		t.Errorf("path form: %v", err)
	}
}

func TestFileStoreMissingIsNotFoundAndDeleteWorks(t *testing.T) {
	r, _ := fileResolver(t)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "file://a/missing"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	_ = r.Set(ctx, "file://a/b", strings.NewReader("value-value-1"))
	if err := r.Delete(ctx, "file://a/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, "file://a/b"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := r.Delete(ctx, "file://a/b"); err != nil {
		t.Fatalf("deleting a missing entry must not fail: %v", err)
	}
}

func TestFileStoreRefusesLoosePermissionsAndEscapingSymlink(t *testing.T) {
	if !runtimeIsUnix() {
		t.Skip("unix permissions")
	}
	r, dir := fileResolver(t)
	ctx := context.Background()
	_ = r.Set(ctx, "file://a/b", strings.NewReader("value-value-1"))
	p := filepath.Join(dir, "a", "b")
	//nolint:gosec // the test makes the entry too permissive on purpose
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, "file://a/b"); secrets.KindOf(err) != secrets.KindInvalid {
		t.Errorf("0644 entry: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(outside, []byte("value-value-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, "file://a/b"); secrets.KindOf(err) != secrets.KindInvalid {
		t.Errorf("escaping symlink: %v", err)
	}
}

func TestStateDirFollowsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	got, err := secrets.StateDir()
	if err != nil || got != filepath.FromSlash("/xdg/state/hollis/secrets") {
		t.Fatalf("StateDir = %q, %v", got, err)
	}
}

func TestKeychainUnavailableOnAHostWithoutSecretServicePointsAtFile(t *testing.T) {
	store := &secretstest.FakeStore{Fail: &secrets.Error{Kind: secrets.KindUnavailable, Source: "keychain", Reason: "no Secret Service"}}
	r := secrets.New(secrets.WithStore(store))
	_, err := r.Resolve(context.Background(), "keychain://openai/work")
	if !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	var se *secrets.Error
	if !errors.As(err, &se) {
		t.Fatal("not an *Error")
	}
	g := se.Guidance()
	if goosIsLinux() && (!strings.Contains(g, "file://") || !strings.Contains(g, "op://")) {
		t.Errorf("guidance %q must point to file:// and op://", g)
	}
}
