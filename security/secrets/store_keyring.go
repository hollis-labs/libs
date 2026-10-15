package secrets

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"
)

// keyringMocked is set once the in-memory keyring has been installed. It is
// never cleared: go-keyring offers no way back to the real provider.
var keyringMocked atomic.Bool

// MockKeyring switches go-keyring to its in-memory provider for the rest of
// the process and lets [NewKeyringStore] run inside a test binary. Tests call
// it through secretstest.Install.
func MockKeyring(tb testing.TB) {
	tb.Helper()
	keyring.MockInit()
	keyringMocked.Store(true)
}

// NewKeyringStore returns the OS credential store: macOS Keychain, Linux
// Secret Service, Windows Credential Manager (through zalando/go-keyring).
//
// Inside a test binary, any use before [MockKeyring] fails with
// KindUnavailable instead of touching the real keychain, so a forgotten setup
// is a loud test failure, not a prompt on a developer's machine.
//
// go-keyring calls take no context. Each call runs in a goroutine so a
// canceled or timed-out ctx returns promptly; the abandoned call finishes in
// the background.
func NewKeyringStore() Store { return keyringStore{} }

type keyringStore struct{}

var errRealStoreInTest = &Error{Kind: KindUnavailable, Reason: "the real OS keychain is refused inside a test binary; call secretstest.Install(t) first"}

func guard() error {
	if testing.Testing() && !keyringMocked.Load() {
		cp := *errRealStoreInTest
		return &cp
	}
	return nil
}

func (keyringStore) Get(ctx context.Context, service, account string) (Secret, error) {
	if err := guard(); err != nil {
		return Secret{}, err
	}
	v, err := inCtx(ctx, func() (string, error) { return keyring.Get(service, account) })
	if err != nil {
		return Secret{}, classifyKeyring(err)
	}
	return FromString(v), nil
}

func (keyringStore) Set(ctx context.Context, service, account string, value Secret) error {
	if err := guard(); err != nil {
		return err
	}
	_, err := inCtx(ctx, func() (string, error) { return "", keyring.Set(service, account, value.Reveal()) })
	return classifyKeyring(err)
}

func (keyringStore) Delete(ctx context.Context, service, account string) error {
	if err := guard(); err != nil {
		return err
	}
	_, err := inCtx(ctx, func() (string, error) { return "", keyring.Delete(service, account) })
	return classifyKeyring(err)
}

func inCtx(ctx context.Context, fn func() (string, error)) (string, error) {
	type result struct {
		v   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// classifyKeyring maps go-keyring errors onto the taxonomy. NotFound and
// too-big are go-keyring sentinels. The Denied heuristic matches the wording
// of the macOS and Secret Service refusals and is UNVERIFIED on each platform;
// anything not recognized is Unavailable, which is the safe answer.
func classifyKeyring(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return &Error{Kind: KindNotFound, Source: "keychain", Reason: "no such entry", Cause: err}
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return &Error{Kind: KindTooLarge, Source: "keychain", Reason: "the value is too large for the credential store", Cause: err}
	case errors.Is(err, keyring.ErrUnsupportedPlatform):
		return &Error{Kind: KindUnavailable, Source: "keychain", Reason: "no credential store on this platform", Cause: err}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return &Error{Kind: KindUnavailable, Source: "keychain", Reason: "the credential store did not answer in time", Cause: err}
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"user cancel", "interaction not allowed", "denied", "locked"} {
		if strings.Contains(text, marker) {
			return &Error{Kind: KindDenied, Source: "keychain", Reason: "the credential store refused access", Cause: err}
		}
	}
	return &Error{Kind: KindUnavailable, Source: "keychain", Reason: "the credential store is unavailable", Cause: err}
}
