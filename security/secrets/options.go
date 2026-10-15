package secrets

import (
	"os"
	"time"
)

// DefaultService is the keychain service name the keychain backend writes to.
// One shared name keeps every app's entries in one place; an adopter with
// existing entries under its own name reads them through [WithLegacy] without
// migrating them.
const DefaultService = "hollis-labs"

// Option configures [New].
type Option func(*config)

type config struct {
	store     Store
	service   string
	legacy    []legacy
	timeout   time.Duration
	sinks     []ValueSink
	audit     func(Event)
	backends  []Backend
	withEnv   bool
	envLookup func(string) (string, bool)
	envFB     func(Ref) []string
	exec      execConfig
	fileRoots []string
	home      func() (string, error)
	stateDir  func() (string, error)
}

func newConfig() *config {
	return &config{
		service:   DefaultService,
		timeout:   DefaultTimeout,
		envLookup: os.LookupEnv,
	}
}

// WithStore replaces the OS credential store behind keychain://.
func WithStore(s Store) Option { return func(c *config) { c.store = s } }

// WithService sets the keychain service name (default [DefaultService]).
func WithService(name string) Option { return func(c *config) { c.service = name } }

// WithLegacy adds a read-through to an older keychain location. When the
// shared service has no entry, the keychain backend asks (service, account(ref))
// before reporting not found, and reports Source "keychain:legacy:<service>".
// Nothing is migrated and nothing is ever written to the legacy location.
// Repeat for several old locations; they are tried in order. An account func that
// returns "" means the old location has no entry for that reference and is skipped.
func WithLegacy(service string, account func(Ref) string) Option {
	return func(c *config) { c.legacy = append(c.legacy, legacy{service: service, account: account}) }
}

// WithTimeout bounds each resolution (default [DefaultTimeout]).
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithRedactor registers a sink that learns every resolved value before the
// caller sees it. Repeatable.
func WithRedactor(s ValueSink) Option { return func(c *config) { c.sinks = append(c.sinks, s) } }

// WithAudit registers a hook called once per resolution with names only.
func WithAudit(fn func(Event)) Option { return func(c *config) { c.audit = fn } }

// WithBackend registers b, replacing a built-in backend for the same scheme
// and its aliases. This is how a secrets service is added.
func WithBackend(b Backend) Option { return func(c *config) { c.backends = append(c.backends, b) } }

// WithEnv enables the env:// scheme. Off by default: the environment is not a
// place secrets should travel. Resolved values report Source "env:<VAR>".
func WithEnv() Option { return func(c *config) { c.withEnv = true } }

// WithEnvLookup replaces os.LookupEnv for env:// and the env fallback.
func WithEnvLookup(fn func(string) (string, bool)) Option {
	return func(c *config) { c.envLookup = fn }
}

// WithEnvFallback opts in to a conventional-variable fallback: when a backend
// reports not found or unavailable, the variables names(ref) returns are tried
// in order. Denied does not fall back. The result reports Source "env:<VAR>".
// This is the container case (OPENAI_API_KEY set, no keychain).
func WithEnvFallback(names func(Ref) []string) Option {
	return func(c *config) { c.envFB = names }
}

// WithCommandRunner replaces how helper and op children are run. For tests.
func WithCommandRunner(run CommandRunner) Option { return func(c *config) { c.exec.run = run } }

// WithLookPath replaces executable lookup for helpers and op. For tests.
func WithLookPath(fn func(name string) (string, error)) Option {
	return func(c *config) { c.exec.lookPath = fn }
}

// WithHelperDirs sets the directories searched after PATH. The default is the
// list a launchd-started service needs, since its PATH is minimal.
func WithHelperDirs(dirs ...string) Option { return func(c *config) { c.exec.dirs = dirs } }

// WithFileRoots sets the directories a file:// symlink may resolve into, in
// addition to the home directory.
func WithFileRoots(roots ...string) Option { return func(c *config) { c.fileRoots = roots } }

// WithHome replaces how ~ is resolved for file://. The default asks the user
// database, not $HOME, because a launch's caller can set $HOME.
func WithHome(fn func() (string, error)) Option { return func(c *config) { c.home = fn } }

// WithStateDir sets the directory of the managed file store (file://<ns>/<key>).
// The default is [StateDir]. For tests and for hosts with a different layout.
func WithStateDir(dir string) Option {
	return func(c *config) { c.stateDir = func() (string, error) { return dir, nil } }
}

func (c *config) build() *Resolver {
	r := &Resolver{
		backends:    map[string]Backend{},
		timeout:     c.timeout,
		sinks:       c.sinks,
		audit:       c.audit,
		envFallback: c.envFB,
		envLookup:   c.envLookup,
	}
	if r.timeout <= 0 {
		r.timeout = DefaultTimeout
	}
	store := c.store
	if store == nil {
		store = NewKeyringStore()
	}
	ex := c.exec.withDefaults()
	r.register(NewKeychainBackend(store, c.service, c.legacy...))
	r.register(&HelperBackend{exec: ex, schemes: r.schemes})
	r.register(&OpBackend{exec: ex, schemes: r.schemes})
	r.register(&FileBackend{roots: c.fileRoots, home: c.home, stateDir: c.stateDir})
	if c.withEnv {
		r.register(&EnvBackend{lookup: c.envLookup})
	}
	for _, b := range c.backends {
		r.register(b)
	}
	return r
}

func (r *Resolver) register(b Backend) {
	names := []string{b.Scheme()}
	if a, ok := b.(Aliaser); ok {
		names = append(names, a.Aliases()...)
	}
	for _, n := range names {
		if _, exists := r.backends[n]; !exists {
			r.order = append(r.order, n)
		}
		r.backends[n] = b
	}
}
