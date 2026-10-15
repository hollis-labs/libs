// Package secretstest is the test kit for code that uses go-secrets.
//
// Install(t) must be the first call in any test that could reach the keychain:
// a test binary that reaches the real OS store without it fails loudly rather
// than prompting on a developer's machine. The fakes let a test choose each
// kind of failure the taxonomy distinguishes.
package secretstest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	secrets "github.com/hollis-labs/go-secrets"
)

// Install switches the keychain to an in-memory provider for the rest of the
// test binary and permits secrets.NewKeyringStore inside it.
func Install(tb testing.TB) {
	tb.Helper()
	secrets.MockKeyring(tb)
}

// FakeStore is an in-memory [secrets.Store] with failure injection.
type FakeStore struct {
	mu      sync.Mutex
	entries map[string]string
	calls   []string
	// Fail, if set, is returned by every call before the entry is looked at.
	// Build it with &secrets.Error{Kind: secrets.KindDenied}.
	Fail error
}

func storeKey(service, account string) string { return service + "\x00" + account }

// Put stores a value directly, as a previous version of an app would have.
func (f *FakeStore) Put(service, account, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[string]string{}
	}
	f.entries[storeKey(service, account)] = value
}

// Calls returns the calls made so far as "get|set|delete service/account".
func (f *FakeStore) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Has reports whether an entry exists.
func (f *FakeStore) Has(service, account string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.entries[storeKey(service, account)]
	return ok
}

// Get implements secrets.Store.
func (f *FakeStore) Get(_ context.Context, service, account string) (secrets.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "get "+service+"/"+account)
	if f.Fail != nil {
		return secrets.Secret{}, f.Fail
	}
	v, ok := f.entries[storeKey(service, account)]
	if !ok {
		return secrets.Secret{}, &secrets.Error{Kind: secrets.KindNotFound, Source: "keychain", Reason: "no such entry"}
	}
	return secrets.FromString(v), nil
}

// Set implements secrets.Store.
func (f *FakeStore) Set(_ context.Context, service, account string, value secrets.Secret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "set "+service+"/"+account)
	if f.Fail != nil {
		return f.Fail
	}
	if f.entries == nil {
		f.entries = map[string]string{}
	}
	f.entries[storeKey(service, account)] = value.Reveal()
	return nil
}

// Delete implements secrets.Store.
func (f *FakeStore) Delete(_ context.Context, service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete "+service+"/"+account)
	if f.Fail != nil {
		return f.Fail
	}
	if _, ok := f.entries[storeKey(service, account)]; !ok {
		return &secrets.Error{Kind: secrets.KindNotFound, Source: "keychain", Reason: "no such entry"}
	}
	delete(f.entries, storeKey(service, account))
	return nil
}

// FakeBackend is a [secrets.Backend] for any scheme, answering from a map keyed
// by the reference as written.
type FakeBackend struct {
	Name   string            // scheme served
	Values map[string]string // ref -> value
	Errs   map[string]error  // ref -> error, checked first
	mu     sync.Mutex
	calls  []string
}

// Scheme implements secrets.Backend.
func (f *FakeBackend) Scheme() string { return f.Name }

// Calls returns the references resolved so far.
func (f *FakeBackend) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Resolve implements secrets.Backend.
func (f *FakeBackend) Resolve(_ context.Context, ref secrets.Ref) (secrets.Resolved, error) {
	f.mu.Lock()
	f.calls = append(f.calls, ref.Raw)
	f.mu.Unlock()
	if err, ok := f.Errs[ref.Raw]; ok {
		return secrets.Resolved{}, err
	}
	v, ok := f.Values[ref.Raw]
	if !ok {
		return secrets.Resolved{}, &secrets.Error{Kind: secrets.KindNotFound, Ref: ref.Raw, Reason: "no such entry"}
	}
	return secrets.Resolved{Secret: secrets.FromString(v), Source: f.Name}, nil
}

// WriteHelper writes an executable POSIX shell script named name into a new
// temp dir and returns the directory, ready for secrets.WithHelperDirs. It
// skips the test on Windows.
func WriteHelper(tb testing.TB, name, script string) string {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skip("shell helper scripts are not supported on windows")
	}
	dir := tb.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil { //nolint:gosec // G306: a test helper must be executable
		tb.Fatal(err)
	}
	return dir
}

// AssertNoLeak fails the test if secret appears in any rendering of any
// surface: fmt with %v %+v %#v %s %q, encoding/json, and log/slog text and
// JSON handlers. Pass the values an application would log or return: the
// Secret, a Resolved, an error, a config struct holding them.
func AssertNoLeak(tb testing.TB, secret string, surfaces ...any) {
	tb.Helper()
	if secret == "" {
		tb.Fatal("AssertNoLeak needs a non-empty secret")
	}
	check := func(how, out string) {
		if strings.Contains(out, secret) {
			tb.Errorf("secret leaked through %s: %s", how, out)
		}
	}
	for i, s := range surfaces {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			check(fmt.Sprintf("surface %d %s", i, verb), fmt.Sprintf(verb, s))
		}
		if b, err := json.Marshal(s); err == nil {
			check(fmt.Sprintf("surface %d json", i), string(b))
		}
		for name, mk := range map[string]func(*bytes.Buffer) slog.Handler{
			"slog text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
			"slog json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		} {
			var buf bytes.Buffer
			slog.New(mk(&buf)).Info("surface", "value", s)
			check(fmt.Sprintf("surface %d %s", i, name), buf.String())
		}
	}
}
