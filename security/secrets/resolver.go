package secrets

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// DefaultTimeout bounds one resolution when [WithTimeout] is not given.
const DefaultTimeout = 15 * time.Second

// Resolved is a successfully resolved secret and where it came from.
type Resolved struct {
	Secret Secret
	// Ref is the reference that was resolved, as written. Safe to log.
	Ref string
	// Source names where the value came from, never the value: "keychain",
	// "keychain:legacy:tether", "helper:name", "file", "op", "env:VAR". It is
	// what lets an operator see that the environment, not the keychain, answered.
	Source string
}

// Backend gives one scheme its meaning. Implementations must return *Error
// values (any other error is wrapped as KindUnavailable) and must not put the
// secret, a helper's stderr or a backend's raw error text into one.
//
// A Backend that fronts a secrets service (Vault, Infisical, our own) is an
// ordinary Backend: the reference grammar does not change.
type Backend interface {
	// Scheme is the primary scheme the backend serves, for example "keychain".
	Scheme() string
	// Resolve fetches the value Ref names. Source may be left empty.
	Resolve(ctx context.Context, ref Ref) (Resolved, error)
}

// Aliaser is implemented by a Backend that also serves other schemes.
type Aliaser interface{ Aliases() []string }

// Writer is implemented by a Backend whose entries can be written.
type Writer interface {
	Set(ctx context.Context, ref Ref, value Secret) error
	Delete(ctx context.Context, ref Ref) error
}

// Store is an OS-level credential store: the surface of a keychain.
type Store interface {
	Get(ctx context.Context, service, account string) (Secret, error)
	Set(ctx context.Context, service, account string, value Secret) error
	Delete(ctx context.Context, service, account string) error
}

// ValueSink learns every secret value a Resolver returns, before the caller
// sees it. redact.Set implements it.
type ValueSink interface{ Add(values ...string) }

// Event describes one resolution for an audit hook. It carries names only.
type Event struct {
	Ref      string
	Source   string
	Kind     Kind // "" on success
	Duration time.Duration
}

// Resolver resolves references through its registered Backends.
type Resolver struct {
	backends map[string]Backend
	order    []string
	timeout  time.Duration
	sinks    []ValueSink
	audit    func(Event)

	envFallback func(Ref) []string
	envLookup   func(string) (string, bool)
}

// New returns a Resolver with the keychain, helper, file and op backends.
// Without options the keychain backend uses the real OS store; a test binary
// that reaches it without secretstest.Install fails loudly instead.
func New(opts ...Option) *Resolver {
	c := newConfig()
	for _, o := range opts {
		o(c)
	}
	return c.build()
}

// Resolve parses ref and resolves it. The returned error is an *Error.
func (r *Resolver) Resolve(ctx context.Context, ref string) (Resolved, error) {
	parsed, err := ParseRef(ref)
	if err != nil {
		return Resolved{}, err
	}
	return r.ResolveRef(ctx, parsed)
}

// ResolveRef resolves an already-parsed reference.
func (r *Resolver) ResolveRef(ctx context.Context, ref Ref) (res Resolved, err error) {
	start := time.Now()
	defer func() {
		if r.audit != nil {
			ev := Event{Ref: ref.Raw, Source: res.Source, Duration: time.Since(start)}
			var se *Error
			if errors.As(err, &se) {
				ev.Kind = se.Kind
				if ev.Source == "" {
					ev.Source = se.Source
				}
			}
			r.audit(ev)
		}
	}()

	b, ok := r.backends[ref.Scheme]
	if !ok {
		return Resolved{}, &Error{Kind: KindUnsupportedScheme, Ref: ref.Scheme + "://…", Reason: "no backend for scheme " + ref.Scheme}
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	res, err = b.Resolve(ctx, ref)
	if err != nil {
		err = normalize(err, ref)
		if k := KindOf(err); (k == KindNotFound || k == KindUnavailable) && r.envFallback != nil {
			if fb, ok := r.fromEnv(ref); ok {
				return r.finish(fb, ref)
			}
		}
		return Resolved{}, err
	}
	return r.finish(res, ref)
}

// finish applies the checks every backend's answer must pass.
func (r *Resolver) finish(res Resolved, ref Ref) (Resolved, error) {
	value := strings.TrimSpace(res.Secret.Reveal())
	if value == "" {
		return Resolved{}, newErr(KindEmpty, ref, res.Source, "the entry holds no value", nil)
	}
	if hasKnownScheme(value, r.schemes()) {
		// A helper that read a reference-valued variable and echoed it back.
		return Resolved{}, newErr(KindResolvedToRef, ref, res.Source, "the backend returned a reference, not a value", nil)
	}
	if res.Source == "" {
		res.Source = ref.Scheme
	}
	for _, s := range r.sinks {
		s.Add(value)
	}
	return Resolved{Secret: FromString(value), Ref: ref.Raw, Source: res.Source}, nil
}

func (r *Resolver) fromEnv(ref Ref) (Resolved, bool) {
	for _, name := range r.envFallback(ref) {
		if v, ok := r.envLookup(name); ok && strings.TrimSpace(v) != "" {
			return Resolved{Secret: FromString(v), Source: "env:" + name}, true
		}
	}
	return Resolved{}, false
}

// Func returns a lazy resolver for ref: nothing is fetched or held until the
// returned function is called, and nothing is kept after it returns. This is
// the shape for a provider key that must not sit in configuration.
func (r *Resolver) Func(ref string) func(context.Context) (Secret, error) {
	return func(ctx context.Context) (Secret, error) {
		res, err := r.Resolve(ctx, ref)
		return res.Secret, err
	}
}

// Set stores the value read from src under ref. The backend must be a [Writer].
// Reading from an io.Reader keeps the value off argv and out of the caller's
// strings until the store needs it.
func (r *Resolver) Set(ctx context.Context, ref string, src io.Reader) error {
	parsed, w, err := r.writer(ref)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(src, 1<<20))
	if err != nil {
		return newErr(KindUnavailable, parsed, "", "could not read the value to store", err)
	}
	v := strings.TrimSpace(string(raw))
	if v == "" {
		return newErr(KindEmpty, parsed, "", "refusing to store an empty value", nil)
	}
	for _, s := range r.sinks {
		s.Add(v)
	}
	return normalize(w.Set(ctx, parsed, FromString(v)), parsed)
}

// Delete removes the entry ref names. A missing entry is not an error.
func (r *Resolver) Delete(ctx context.Context, ref string) error {
	parsed, w, err := r.writer(ref)
	if err != nil {
		return err
	}
	if err := normalize(w.Delete(ctx, parsed), parsed); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (r *Resolver) writer(ref string) (Ref, Writer, error) {
	parsed, err := ParseRef(ref)
	if err != nil {
		return Ref{}, nil, err
	}
	b, ok := r.backends[parsed.Scheme]
	if !ok {
		return Ref{}, nil, &Error{Kind: KindUnsupportedScheme, Ref: parsed.Scheme + "://…", Reason: "no backend for scheme " + parsed.Scheme}
	}
	w, ok := b.(Writer)
	if !ok {
		return Ref{}, nil, newErr(KindInvalid, parsed, "", "the "+parsed.Scheme+" backend cannot store values", nil)
	}
	return parsed, w, nil
}

// Schemes returns the registered schemes, aliases included, in registration order.
func (r *Resolver) Schemes() []string { return r.schemes() }

func (r *Resolver) schemes() []string { return append([]string(nil), r.order...) }

// normalize turns any error into an *Error that carries ref and never renders
// the original text.
func normalize(err error, ref Ref) error {
	if err == nil {
		return nil
	}
	var se *Error
	if errors.As(err, &se) {
		if se.Ref == "" {
			cp := *se
			cp.Ref = ref.Raw
			return &cp
		}
		return err
	}
	kind, reason := KindUnavailable, "the backend failed"
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "the backend timed out"
	} else if errors.Is(err, context.Canceled) {
		reason = "the lookup was canceled"
	}
	return newErr(kind, ref, "", reason, err)
}

// environ returns the process environment, the default source for helper children.
var environ = os.Environ
