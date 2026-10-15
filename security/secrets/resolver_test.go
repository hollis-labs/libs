package secrets_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	secrets "github.com/hollis-labs/go-secrets"
	"github.com/hollis-labs/go-secrets/redact"
	"github.com/hollis-labs/go-secrets/secretstest"
)

var ctx = context.Background()

func mustResolve(t *testing.T, r *secrets.Resolver, ref string) secrets.Resolved {
	t.Helper()
	got, err := r.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve(%q) = %v", ref, err)
	}
	return got
}

func TestKeychainUsesTheSharedServiceAndKeyAccount(t *testing.T) {
	store := &secretstest.FakeStore{}
	store.Put(secrets.DefaultService, "openai/work", value)
	r := secrets.New(secrets.WithStore(store))

	got := mustResolve(t, r, "keychain://openai/work")
	if got.Secret.Reveal() != value || got.Source != "keychain" || got.Ref != "keychain://openai/work" {
		t.Fatalf("got %+v", got)
	}
	if got := mustResolve(t, r, "keyring://openai/work"); got.Secret.Reveal() != value {
		t.Fatal("keyring:// must be an alias of keychain://")
	}
}

func TestKeychainReadsThroughToLegacyNamesWithoutMigrating(t *testing.T) {
	store := &secretstest.FakeStore{}
	store.Put("tether", "provider-api-key:openai/work", value)
	r := secrets.New(secrets.WithStore(store),
		secrets.WithLegacy("tether", func(ref secrets.Ref) string { return "provider-api-key:" + ref.Key() }))

	got := mustResolve(t, r, "keychain://openai/work")
	if got.Secret.Reveal() != value || got.Source != "keychain:legacy:tether" {
		t.Fatalf("got %+v", got)
	}
	if store.Has(secrets.DefaultService, "openai/work") {
		t.Fatal("read-through must not copy the entry into the shared service")
	}
	for _, c := range store.Calls() {
		if strings.HasPrefix(c, "set") || strings.HasPrefix(c, "delete") {
			t.Fatalf("read-through wrote: %v", store.Calls())
		}
	}
}

func TestKeychainPrefersTheSharedEntryOverLegacy(t *testing.T) {
	store := &secretstest.FakeStore{}
	store.Put(secrets.DefaultService, "a/b", "new-value-123")
	store.Put("old", "a/b", "old-value-123")
	r := secrets.New(secrets.WithStore(store), secrets.WithLegacy("old", func(ref secrets.Ref) string { return ref.Key() }))
	if got := mustResolve(t, r, "keychain://a/b"); got.Secret.Reveal() != "new-value-123" || got.Source != "keychain" {
		t.Fatalf("got %+v", got)
	}
}

func TestKeychainStopsAtLegacyDenial(t *testing.T) {
	store := &secretstest.FakeStore{}
	r := secrets.New(secrets.WithStore(store))
	store.Fail = &secrets.Error{Kind: secrets.KindDenied}
	_, err := r.Resolve(ctx, "keychain://a/b")
	if !errors.Is(err, secrets.ErrDenied) {
		t.Fatalf("err = %v, want KindDenied, not a quiet not-found", err)
	}
}

func TestSetReadsFromAReaderAndDeleteIsIdempotent(t *testing.T) {
	store := &secretstest.FakeStore{}
	r := secrets.New(secrets.WithStore(store))
	if err := r.Set(ctx, "keychain://a/b", strings.NewReader("  "+value+"\n")); err != nil {
		t.Fatal(err)
	}
	if got := mustResolve(t, r, "keychain://a/b"); got.Secret.Reveal() != value {
		t.Fatalf("stored value = %q, want it trimmed", got.Secret.Reveal())
	}
	if err := r.Delete(ctx, "keychain://a/b"); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "keychain://a/b"); err != nil {
		t.Fatalf("second delete = %v, want nil", err)
	}
	if err := r.Set(ctx, "keychain://a/b", strings.NewReader("\n")); !errors.Is(err, secrets.ErrEmpty) {
		t.Fatalf("empty Set err = %v", err)
	}
	if err := r.Set(ctx, "file:///x", strings.NewReader(value)); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("Set on a read-only scheme err = %v, want KindInvalid", err)
	}
}

func TestErrorTaxonomyAndSentinels(t *testing.T) {
	store := &secretstest.FakeStore{}
	r := secrets.New(secrets.WithStore(store))
	cases := []struct {
		fail error
		want secrets.Kind
	}{
		{nil, secrets.KindNotFound},
		{&secrets.Error{Kind: secrets.KindDenied}, secrets.KindDenied},
		{&secrets.Error{Kind: secrets.KindUnavailable}, secrets.KindUnavailable},
		{errors.New("raw backend failure"), secrets.KindUnavailable},
	}
	for _, c := range cases {
		store.Fail = c.fail
		_, err := r.Resolve(ctx, "keychain://a/b")
		if got := secrets.KindOf(err); got != c.want {
			t.Errorf("fail=%v: kind = %q, want %q (err %v)", c.fail, got, c.want, err)
		}
		var se *secrets.Error
		if !errors.As(err, &se) {
			t.Fatalf("err %T is not *secrets.Error", err)
		}
		if se.Ref != "keychain://a/b" || se.Code() != "secret_"+string(c.want) || se.Guidance() == "" {
			t.Errorf("error fields: %+v code=%q guidance=%q", se, se.Code(), se.Guidance())
		}
	}
	for _, k := range []secrets.Kind{secrets.KindNotFound, secrets.KindDenied, secrets.KindUnavailable, secrets.KindInvalid,
		secrets.KindEmpty, secrets.KindResolvedToRef, secrets.KindTooLarge, secrets.KindUnsupportedScheme} {
		if (&secrets.Error{Kind: k}).Guidance() == "" {
			t.Errorf("no guidance for %s", k)
		}
	}
}

func TestErrorTextNeverRendersTheCauseButKeepsItReachable(t *testing.T) {
	cause := errors.New("backend said: token=SECRET-CAUSE-TEXT")
	store := &secretstest.FakeStore{Fail: cause}
	r := secrets.New(secrets.WithStore(store))
	_, err := r.Resolve(ctx, "keychain://a/b")
	if strings.Contains(err.Error(), "SECRET-CAUSE-TEXT") {
		t.Fatalf("error renders its cause: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is must still reach the cause")
	}
	secretstest.AssertNoLeak(t, "SECRET-CAUSE-TEXT", err)
}

func TestUnsupportedSchemeAndLiteralNeverEchoCredentials(t *testing.T) {
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}))
	_, err := r.Resolve(ctx, "postgres://user:hunter2@host/db")
	if !errors.Is(err, secrets.ErrUnsupportedScheme) || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v, want unsupported scheme without the credential", err)
	}
	_, err = r.Resolve(ctx, "sk-live-0123456789abcdef")
	if !errors.Is(err, secrets.ErrInvalid) || strings.Contains(err.Error(), "sk-live") {
		t.Fatalf("a literal passed by mistake: err = %v", err)
	}
}

func TestEnvIsOffByDefaultAndExplicitWhenOn(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "MY_KEY" {
			return value, true
		}
		return "", false
	}
	store := &secretstest.FakeStore{}
	off := secrets.New(secrets.WithStore(store), secrets.WithEnvLookup(lookup))
	if _, err := off.Resolve(ctx, "env://MY_KEY"); !errors.Is(err, secrets.ErrUnsupportedScheme) {
		t.Fatalf("env:// without WithEnv: err = %v", err)
	}
	on := secrets.New(secrets.WithStore(store), secrets.WithEnv(), secrets.WithEnvLookup(lookup))
	got := mustResolve(t, on, "env://MY_KEY")
	if got.Secret.Reveal() != value || got.Source != "env:MY_KEY" {
		t.Fatalf("got %+v", got)
	}
	if _, err := on.Resolve(ctx, "env://UNSET"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("unset var err = %v", err)
	}
	if _, err := on.Resolve(ctx, "env://MY_KEY/extra"); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("env with a path err = %v", err)
	}
}

func TestEnvFallbackIsOptInAndReportsItsSource(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "OPENAI_API_KEY" {
			return value, true
		}
		return "", false
	}
	names := func(ref secrets.Ref) []string { return []string{strings.ToUpper(ref.Authority) + "_API_KEY"} }
	store := &secretstest.FakeStore{}

	plain := secrets.New(secrets.WithStore(store), secrets.WithEnvLookup(lookup))
	if _, err := plain.Resolve(ctx, "keychain://openai/work"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("no silent env fallback: err = %v", err)
	}

	r := secrets.New(secrets.WithStore(store), secrets.WithEnvLookup(lookup), secrets.WithEnvFallback(names))
	got := mustResolve(t, r, "keychain://openai/work")
	if got.Secret.Reveal() != value || got.Source != "env:OPENAI_API_KEY" {
		t.Fatalf("got %+v", got)
	}
	store.Fail = &secrets.Error{Kind: secrets.KindUnavailable}
	if got := mustResolve(t, r, "keychain://openai/work"); got.Source != "env:OPENAI_API_KEY" {
		t.Fatalf("headless fallback source = %q", got.Source)
	}
	store.Fail = &secrets.Error{Kind: secrets.KindDenied}
	if _, err := r.Resolve(ctx, "keychain://openai/work"); !errors.Is(err, secrets.ErrDenied) {
		t.Fatalf("denied must not fall back to the environment: err = %v", err)
	}
}

func TestResolvedValueReachesTheRedactorBeforeTheCaller(t *testing.T) {
	var set redact.Set
	var events []secrets.Event
	store := &secretstest.FakeStore{}
	store.Put(secrets.DefaultService, "a/b", value)
	r := secrets.New(secrets.WithStore(store), secrets.WithRedactor(&set), secrets.WithAudit(func(e secrets.Event) { events = append(events, e) }))

	mustResolve(t, r, "keychain://a/b")
	if got := set.Redact("auth failed for " + value); strings.Contains(got, value) {
		t.Fatalf("redactor did not learn the value: %q", got)
	}
	if _, err := r.Resolve(ctx, "keychain://missing/key"); err == nil {
		t.Fatal("want not found")
	}
	if len(events) != 2 || events[0].Source != "keychain" || events[0].Kind != "" || events[1].Kind != secrets.KindNotFound {
		t.Fatalf("events = %+v", events)
	}
	secretstest.AssertNoLeak(t, value, events[0], events[1])
}

func TestBackendsAreExtensibleWithoutChangingTheGrammar(t *testing.T) {
	vault := &secretstest.FakeBackend{Name: "vault", Values: map[string]string{"vault://secret/data/app": value}}
	override := &secretstest.FakeBackend{Name: "helper", Values: map[string]string{"helper://x/y": "from-fake-helper"}}
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithBackend(vault), secrets.WithBackend(override))

	if got := mustResolve(t, r, "vault://secret/data/app"); got.Secret.Reveal() != value || got.Source != "vault" {
		t.Fatalf("got %+v", got)
	}
	if got := mustResolve(t, r, "helper://x/y"); got.Secret.Reveal() != "from-fake-helper" {
		t.Fatal("WithBackend must replace a built-in scheme")
	}
	if _, err := r.Resolve(ctx, "vault://secret/missing"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(r.Schemes(), ","); !strings.Contains(got, "vault") || !strings.Contains(got, "keyring") {
		t.Fatalf("Schemes() = %s", got)
	}
}

func TestBackendReturningAReferenceIsRefused(t *testing.T) {
	echo := &secretstest.FakeBackend{Name: "vault", Values: map[string]string{"vault://a/b": "keychain://a/b"}}
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithBackend(echo))
	if _, err := r.Resolve(ctx, "vault://a/b"); !errors.Is(err, secrets.ErrResolvedToRef) {
		t.Fatalf("err = %v, want KindResolvedToRef", err)
	}
}

func TestFuncIsLazyAndHoldsNothing(t *testing.T) {
	store := &secretstest.FakeStore{}
	store.Put(secrets.DefaultService, "a/b", value)
	r := secrets.New(secrets.WithStore(store))
	get := r.Func("keychain://a/b")
	if len(store.Calls()) != 0 {
		t.Fatal("Func must not fetch until called")
	}
	s, err := get(ctx)
	if err != nil || s.Reveal() != value {
		t.Fatalf("got %v, %v", s, err)
	}
	store.Put(secrets.DefaultService, "a/b", "rotated-value-123")
	if s, _ := get(ctx); s.Reveal() != "rotated-value-123" {
		t.Fatal("Func must re-resolve each call, so rotation needs no restart")
	}
}

func TestFileScheme(t *testing.T) {
	home := t.TempDir()
	write := func(name string, mode os.FileMode, body string) {
		p := filepath.Join(home, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("tok", 0o600, "  "+value+"\n")
	write("loose", 0o644, value)
	write("empty", 0o600, "\n")
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithStateDir(filepath.Join(home, "state")), secrets.WithHome(func() (string, error) { return home, nil }))

	for _, ref := range []string{"file://~/tok", "file://" + filepath.Join(home, "tok")} {
		if got := mustResolve(t, r, ref); got.Secret.Reveal() != value || got.Source != "file" {
			t.Fatalf("%s: got %+v", ref, got)
		}
	}
	for ref, want := range map[string]error{
		"file://~/loose":                         secrets.ErrInvalid,
		"file://~/empty":                         secrets.ErrEmpty,
		"file://~/missing":                       secrets.ErrNotFound,
		"file://relative":                        secrets.ErrInvalid, // managed form needs <ns>/<key>
		"file://" + filepath.Join(home, "$HOME"): secrets.ErrNotFound,
	} {
		_, err := r.Resolve(ctx, ref)
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", ref, err, want)
		}
		if err != nil && strings.Contains(err.Error(), value) {
			t.Errorf("%s: error leaks the value", ref)
		}
	}
}

func TestHelperProtocol(t *testing.T) {
	dir := secretstest.WriteHelper(t, "my-helper", `
printf '%s\n' "$@" > "$ARGS_PATH"
printf 'sk-helper-456\n'
`)
	args := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("ARGS_PATH", args)
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(dir))

	got := mustResolve(t, r, "helper://my-helper/openai/default")
	if got.Secret.Reveal() != "sk-helper-456" || got.Source != "helper:my-helper" {
		t.Fatalf("got %+v", got)
	}
	b, _ := os.ReadFile(args) //nolint:gosec // G304: a path this test created
	if string(b) != "resolve\nkeychain://openai/default\n" {
		t.Fatalf("helper argv = %q", b)
	}
}

func TestHelperExitCodesMapToKinds(t *testing.T) {
	for code, want := range map[string]error{
		"2": secrets.ErrNotFound, "3": secrets.ErrDenied, "1": secrets.ErrUnavailable, "17": secrets.ErrUnavailable,
	} {
		dir := secretstest.WriteHelper(t, "h", "echo 'lookup failed: token=STDERR-SECRET' >&2\nexit "+code+"\n")
		r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(dir))
		_, err := r.Resolve(ctx, "helper://h/a")
		if !errors.Is(err, want) {
			t.Errorf("exit %s: err = %v, want %v", code, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "STDERR-SECRET") || strings.Contains(err.Error(), "lookup failed")) {
			t.Errorf("exit %s: helper stderr reached the error: %v", code, err)
		}
	}
}

func TestHelperProblems(t *testing.T) {
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(t.TempDir()), secrets.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }))
	if _, err := r.Resolve(ctx, "helper://no-such-helper/a"); !errors.Is(err, secrets.ErrUnavailable) || !strings.Contains(err.Error(), "no-such-helper") {
		t.Fatalf("missing helper: err = %v", err)
	}
	for _, ref := range []string{"helper://../a", "helper://./a"} {
		if _, err := r.Resolve(ctx, ref); !errors.Is(err, secrets.ErrInvalid) {
			t.Errorf("%s: err = %v, want KindInvalid", ref, err)
		}
	}
	if _, err := r.Resolve(ctx, "helper://onlyname"); !errors.Is(err, secrets.ErrInvalid) {
		t.Errorf("helper without a path: err = %v", err)
	}

	dir := secretstest.WriteHelper(t, "empty", "printf '\\n'\n")
	r = secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(dir))
	if _, err := r.Resolve(ctx, "helper://empty/a"); !errors.Is(err, secrets.ErrEmpty) {
		t.Errorf("empty output: err = %v", err)
	}
	dir = secretstest.WriteHelper(t, "echoer", "printf '%s' \"$2\"\n")
	r = secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(dir))
	if _, err := r.Resolve(ctx, "helper://echoer/a/b"); !errors.Is(err, secrets.ErrResolvedToRef) {
		t.Errorf("echoed reference: err = %v", err)
	}
	dir = secretstest.WriteHelper(t, "big", "head -c 200000 /dev/zero | tr '\\0' x\n")
	r = secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(dir))
	if _, err := r.Resolve(ctx, "helper://big/a"); !errors.Is(err, secrets.ErrTooLarge) {
		t.Errorf("oversized output: err = %v", err)
	}
}

func TestHelperChildNeverSeesReferenceValuedVariables(t *testing.T) {
	t.Setenv("PROVIDER_KEY", "keychain://openai/work")
	t.Setenv("PLAIN_VAR", "plain-value-123")
	dir := secretstest.WriteHelper(t, "envprobe", `printf '%s|%s' "${PROVIDER_KEY:-stripped}" "${PLAIN_VAR:-missing}"`+"\n")
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(dir))
	if got := mustResolve(t, r, "helper://envprobe/a"); got.Secret.Reveal() != "stripped|plain-value-123" {
		t.Fatalf("child saw %q", got.Secret.Reveal())
	}
}

func TestHelperTimeoutReturnsPromptly(t *testing.T) {
	dir := secretstest.WriteHelper(t, "slow", "sleep 10\n")
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(dir), secrets.WithTimeout(200*time.Millisecond))
	start := time.Now()
	_, err := r.Resolve(ctx, "helper://slow/a")
	if !errors.Is(err, secrets.ErrUnavailable) || !strings.Contains(err.Error(), "in time") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %v", d)
	}
}

func TestOpRunsTheCLIWithoutAShell(t *testing.T) {
	var gotPath string
	var gotArgs []string
	run := func(_ context.Context, path string, args []string, _ []string) ([]byte, int, error) {
		gotPath, gotArgs = path, args
		return []byte(value + "\n"), 0, nil
	}
	r := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithCommandRunner(run),
		secrets.WithLookPath(func(n string) (string, error) { return "/fake/" + n, nil }))
	got := mustResolve(t, r, "op://Personal/My Item/password")
	if got.Secret.Reveal() != value || got.Source != "op" {
		t.Fatalf("got %+v", got)
	}
	if gotPath != "/fake/op" || strings.Join(gotArgs, "|") != "read|--no-newline|op://Personal/My Item/password" {
		t.Fatalf("ran %s %q", gotPath, gotArgs)
	}

	missing := secrets.New(secrets.WithStore(&secretstest.FakeStore{}), secrets.WithHelperDirs(t.TempDir()),
		secrets.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }))
	if _, err := missing.Resolve(ctx, "op://v/i/f"); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("op not installed: err = %v", err)
	}
	if _, err := r.Resolve(ctx, "op://onlyvault"); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("op without a path: err = %v", err)
	}
}

func TestAcceptanceTheValueReachesNoSurface(t *testing.T) {
	var set redact.Set
	store := &secretstest.FakeStore{}
	store.Put(secrets.DefaultService, "a/b", value)
	r := secrets.New(secrets.WithStore(store), secrets.WithRedactor(&set))
	got := mustResolve(t, r, "keychain://a/b")
	_, notFound := r.Resolve(ctx, "keychain://a/missing")
	secretstest.AssertNoLeak(t, value, got, got.Secret, notFound, set.Error(errors.New("failed with "+value)))
}

func TestKeychainSkipsALegacyLocationThatHasNoNameForTheReference(t *testing.T) {
	store := &secretstest.FakeStore{}
	r := secrets.New(secrets.WithStore(store),
		secrets.WithLegacy("old", func(secrets.Ref) string { return "" }))

	_, err := r.Resolve(context.Background(), "keychain://openai/work")
	if secrets.KindOf(err) != secrets.KindNotFound {
		t.Fatalf("got %v, want not_found", err)
	}
	for _, c := range store.Calls() {
		if strings.Contains(c, "old") {
			t.Fatalf("an empty legacy account must not reach the store: %v", store.Calls())
		}
	}
}
