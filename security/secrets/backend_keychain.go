package secrets

import "context"

type legacy struct {
	service string
	account func(Ref) string
}

// KeychainBackend serves keychain://<namespace>/<key>[/<more>] (and the alias
// keyring://) from a [Store].
//
// An entry lives at service [DefaultService] (or [WithService]) under the
// account "<namespace>/<key>[/<more>]". Legacy locations given by [WithLegacy]
// are read, never written, when the shared service has no entry.
type KeychainBackend struct {
	store   Store
	service string
	legacy  []legacy
}

// NewKeychainBackend returns a keychain backend over store.
func NewKeychainBackend(store Store, service string, legacy ...legacy) *KeychainBackend {
	if service == "" {
		service = DefaultService
	}
	return &KeychainBackend{store: store, service: service, legacy: legacy}
}

// Scheme implements Backend.
func (*KeychainBackend) Scheme() string { return "keychain" }

// Aliases implements Aliaser.
func (*KeychainBackend) Aliases() []string { return []string{"keyring"} }

func (b *KeychainBackend) account(ref Ref) (string, error) {
	if err := ref.require(true, true); err != nil {
		return "", err
	}
	return ref.Key(), nil
}

// Resolve implements Backend.
func (b *KeychainBackend) Resolve(ctx context.Context, ref Ref) (Resolved, error) {
	account, err := b.account(ref)
	if err != nil {
		return Resolved{}, err
	}
	v, err := b.store.Get(ctx, b.service, account)
	if err == nil {
		return Resolved{Secret: v, Source: "keychain"}, nil
	}
	if KindOf(err) != KindNotFound {
		return Resolved{}, normalize(err, ref)
	}
	for _, l := range b.legacy {
		lv, lerr := b.store.Get(ctx, l.service, l.account(ref))
		if lerr == nil {
			return Resolved{Secret: lv, Source: "keychain:legacy:" + l.service}, nil
		}
		if KindOf(lerr) != KindNotFound {
			return Resolved{}, normalize(lerr, ref)
		}
	}
	return Resolved{}, normalize(err, ref)
}

// Set implements Writer. It writes the shared service only.
func (b *KeychainBackend) Set(ctx context.Context, ref Ref, value Secret) error {
	account, err := b.account(ref)
	if err != nil {
		return err
	}
	return b.store.Set(ctx, b.service, account, value)
}

// Delete implements Writer. It deletes from the shared service only.
func (b *KeychainBackend) Delete(ctx context.Context, ref Ref) error {
	account, err := b.account(ref)
	if err != nil {
		return err
	}
	return b.store.Delete(ctx, b.service, account)
}
