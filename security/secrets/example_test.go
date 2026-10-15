package secrets_test

import (
	"context"
	"errors"
	"fmt"

	secrets "github.com/hollis-labs/go-secrets"
	"github.com/hollis-labs/go-secrets/redact"
	"github.com/hollis-labs/go-secrets/secretstest"
)

func Example() {
	store := &secretstest.FakeStore{} // a test double; production code uses the default OS keychain
	store.Put(secrets.DefaultService, "openai/work", "sk-live-0123456789abcdef")

	var known redact.Set
	r := secrets.New(secrets.WithStore(store), secrets.WithRedactor(&known))

	res, err := r.Resolve(context.Background(), "keychain://openai/work")
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Source, res.Secret) // the Secret prints as [redacted]
	fmt.Println(known.Redact("401 for key " + res.Secret.Reveal()))

	_, err = r.Resolve(context.Background(), "keychain://openai/other")
	var se *secrets.Error
	if errors.As(err, &se) {
		fmt.Println(se.Code(), errors.Is(err, secrets.ErrNotFound))
	}
	// Output:
	// keychain [redacted]
	// 401 for key [redacted]
	// secret_not_found true
}
