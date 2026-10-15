package secrets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestRealStoreIsRefusedInsideATestBinaryUntilMocked(t *testing.T) {
	was := keyringMocked.Load()
	t.Cleanup(func() { keyringMocked.Store(was) })
	keyringMocked.Store(false)
	// The guard answers before go-keyring is reached, so nothing touches the
	// keychain even though the provider may already be the mock.
	_, err := NewKeyringStore().Get(context.Background(), "svc", "acct")
	if KindOf(err) != KindUnavailable {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if err := NewKeyringStore().Set(context.Background(), "svc", "acct", FromString("v")); KindOf(err) != KindUnavailable {
		t.Fatalf("Set err = %v", err)
	}
	if err := NewKeyringStore().Delete(context.Background(), "svc", "acct"); KindOf(err) != KindUnavailable {
		t.Fatalf("Delete err = %v", err)
	}
}

func TestKeyringStoreOverTheMockRoundTripsAndClassifies(t *testing.T) {
	MockKeyring(t)
	s := NewKeyringStore()
	ctx := context.Background()
	if _, err := s.Get(ctx, "svc", "a/b"); KindOf(err) != KindNotFound {
		t.Fatalf("missing entry: %v", err)
	}
	if err := s.Set(ctx, "svc", "a/b", FromString("v-value-123")); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get(ctx, "svc", "a/b"); err != nil || v.Reveal() != "v-value-123" {
		t.Fatalf("got %v, %v", v, err)
	}
	if err := s.Delete(ctx, "svc", "a/b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "svc", "a/b"); KindOf(err) != KindNotFound {
		t.Fatalf("second delete: %v", err)
	}
}

func TestClassifyKeyring(t *testing.T) {
	for _, c := range []struct {
		err  error
		want Kind
	}{
		{keyring.ErrNotFound, KindNotFound},
		{keyring.ErrSetDataTooBig, KindTooLarge},
		{keyring.ErrUnsupportedPlatform, KindUnavailable},
		{context.DeadlineExceeded, KindUnavailable},
		{errors.New("User canceled the authorization"), KindDenied},
		{errors.New("The user name or passphrase you entered is not correct; keychain is locked"), KindDenied},
		{errors.New("dbus: connection refused"), KindUnavailable},
	} {
		if got := KindOf(classifyKeyring(c.err)); got != c.want {
			t.Errorf("classifyKeyring(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	if classifyKeyring(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestInCtxReturnsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	_, err := inCtx(ctx, func() (string, error) { <-block; return "", nil })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}
