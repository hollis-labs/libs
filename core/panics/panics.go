package panics

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// ExitSoftware is the conventional process exit status (EX_SOFTWARE, 70) for a
// failure of the program itself rather than of its input.
const ExitSoftware = 70

// Limits on what a [Record] keeps. A pathological panic value or stack cannot
// grow a log line or a database row without bound.
const (
	DefaultMaxValue = 2048
	DefaultMaxStack = 8192
	maxChainDepth   = 16
)

// Record is what is kept about one panic. Value and Stack are already passed
// through the configured redactor, stripped of control characters and bounded.
type Record struct {
	Ref   string
	At    time.Time
	Where string            // the boundary that caught it, for example "http POST /v1/exec"
	Value string            // redacted, or withheld when no redactor is configured
	Stack string            // redacted, bounded; for the operator only
	Attrs map[string]string // caller context attached with [With], for example run=...
}

// Error is a recovered panic as an error. Error() is safe to show to any caller:
// it carries the ref and nothing from the panic.
type Error struct{ Rec Record }

func (e *Error) Error() string { return fmt.Sprintf("internal error (ref %s)", e.Rec.Ref) }

// Ref extracts the ref from err, if err is or wraps an [Error].
func Ref(err error) (string, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Rec.Ref, true
	}
	return "", false
}

// Reporter receives each recovered panic. Implementations should not panic; the
// package guards against it anyway.
type Reporter interface{ Report(Record) }

// ReporterFunc adapts a function to a [Reporter].
type ReporterFunc func(Record)

// Report implements [Reporter].
func (f ReporterFunc) Report(r Record) { f(r) }

// Multi reports to every reporter in order, each isolated from the others'
// failures.
func Multi(rs ...Reporter) Reporter {
	return ReporterFunc(func(r Record) {
		for _, rep := range rs {
			func() {
				defer func() { _ = recover() }()
				rep.Report(r)
			}()
		}
	})
}

// StderrReporter writes one line per panic to w (os.Stderr if w is nil): ref,
// boundary and the sanitized value. The stack is left to the application's own
// reporter, which can store it.
func StderrReporter(w io.Writer) Reporter {
	return ReporterFunc(func(r Record) {
		if w == nil {
			println("panic recovered", r.Ref, "in", r.Where+":", r.Value)
			return
		}
		fmt.Fprintf(w, "panic recovered %s in %s: %s\n", r.Ref, r.Where, r.Value)
	})
}

// Config configures a [Guard].
type Config struct {
	// Reporter receives each panic. Nil reports to standard error.
	Reporter Reporter
	// Redact scrubs untrusted text (the panic value and the stack) before it is
	// recorded. Nil means the value is withheld. Use [Unredacted] to record values
	// without scrubbing, an explicit choice for development.
	Redact func(string) string
	// MaxValue and MaxStack bound the recorded text, in runes. Zero uses the
	// defaults.
	MaxValue, MaxStack int
}

// Unredacted is a [Config.Redact] that records text as it is (still stripped of
// control characters and bounded). It exists so that choosing to record raw
// panic values is a visible decision, not an accident.
func Unredacted(s string) string { return s }

// Guard contains panics according to one [Config]. The package-level functions
// use the default Guard; create your own with [New] for isolation (tests, or a
// subsystem with its own reporter).
type Guard struct {
	cfg Config
}

// New returns a Guard.
func New(cfg Config) *Guard {
	if cfg.Reporter == nil {
		cfg.Reporter = StderrReporter(nil)
	}
	if cfg.MaxValue <= 0 {
		cfg.MaxValue = DefaultMaxValue
	}
	if cfg.MaxStack <= 0 {
		cfg.MaxStack = DefaultMaxStack
	}
	return &Guard{cfg: cfg}
}

var def atomic.Pointer[Guard]

func init() { def.Store(New(Config{})) }

// SetDefault replaces the Guard used by the package-level functions. Call it
// once, early, from main.
func SetDefault(cfg Config) { def.Store(New(cfg)) }

// Default returns the Guard used by the package-level functions.
func Default() *Guard { return def.Load() }

// Option adds context to a captured panic.
type Option func(*Record)

// With attaches a key/value to the record, for example With("run", id). Keys and
// values are sanitized like the rest of the record.
func With(key, value string) Option {
	return func(r *Record) {
		if r.Attrs == nil {
			r.Attrs = map[string]string{}
		}
		r.Attrs[key] = value
	}
}

// Capture builds the record for a recovered value, hands it to the reporter and
// returns it as an [Error]. Call it with the value recover() returned.
func (g *Guard) Capture(where string, v any, opts ...Option) *Error {
	rec := Record{Ref: newRef(), At: time.Now(), Where: g.text(where, 256)}
	for _, o := range opts {
		o(&rec)
	}
	for k, val := range rec.Attrs {
		rec.Attrs[k] = g.text(val, 256)
	}
	rec.Value = g.value(v)
	rec.Stack = g.text(string(debug.Stack()), g.cfg.MaxStack)
	pe := &Error{Rec: rec}
	func() {
		defer func() { _ = recover() }() // a failing reporter must not turn one panic into two
		g.cfg.Reporter.Report(rec)
	}()
	return pe
}

// Call runs fn and converts a panic in it into an [Error].
func (g *Guard) Call(where string, fn func() error, opts ...Option) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = g.Capture(where, v, opts...)
		}
	}()
	return fn()
}

// Recover is for use as "defer g.Recover(where, &err)" in a function with a
// named error result: a panic becomes *errp.
func (g *Guard) Recover(where string, errp *error, opts ...Option) {
	if v := recover(); v != nil {
		pe := g.Capture(where, v, opts...)
		if errp != nil {
			*errp = pe
		}
	}
}

// Go runs fn in a new goroutine that cannot take the process down. A panic is
// captured and, if onPanic is non-nil, handed to it (to mark a record failed, to
// restart a loop). onPanic runs after fn's deferred calls; a panic in onPanic is
// contained too.
func (g *Guard) Go(where string, fn func(), onPanic func(*Error), opts ...Option) {
	go func() {
		defer func() {
			if v := recover(); v != nil {
				pe := g.Capture(where, v, opts...)
				if onPanic != nil {
					func() {
						defer func() { _ = recover() }()
						onPanic(pe)
					}()
				}
			}
		}()
		fn()
	}()
}

// Package-level forms, using the default Guard.

// Capture is [Guard.Capture] on the default Guard.
func Capture(where string, v any, opts ...Option) *Error { return Default().Capture(where, v, opts...) }

// Call is [Guard.Call] on the default Guard.
func Call(where string, fn func() error, opts ...Option) error {
	return Default().Call(where, fn, opts...)
}

// Go is [Guard.Go] on the default Guard.
func Go(where string, fn func(), onPanic func(*Error), opts ...Option) {
	Default().Go(where, fn, onPanic, opts...)
}

// Recover is [Guard.Recover] on the default Guard. It must be deferred directly:
//
//	defer panics.Recover("where", &err)
func Recover(where string, errp *error, opts ...Option) {
	// recover() only works when called by the deferred function itself, so this
	// duplicates Guard.Recover's body rather than delegating to it.
	if v := recover(); v != nil {
		pe := Default().Capture(where, v, opts...)
		if errp != nil {
			*errp = pe
		}
	}
}

func newRef() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "pnc_" + hex.EncodeToString(b[:])
}

// value renders a recovered value and passes it through the redactor. It never
// trusts the value: its String/Error method can itself panic.
func (g *Guard) value(v any) string {
	if g.cfg.Redact == nil {
		return fmt.Sprintf("(value withheld: no redactor configured; type %T)", v)
	}
	return g.text(describe(v), g.cfg.MaxValue)
}

// text sanitizes untrusted text: redacted, control/format characters replaced,
// bounded to limit runes without splitting one.
func (g *Guard) text(s string, limit int) (out string) {
	defer func() {
		if recover() != nil { // a faulty redactor must not leak the unredacted text
			out = "(text withheld: redactor failed)"
		}
	}()
	if g.cfg.Redact != nil {
		s = g.cfg.Redact(s)
	}
	return Clean(s, limit)
}

// describe renders any recovered value without trusting it.
func describe(v any) (out string) {
	defer func() {
		if recover() != nil {
			out = "<value could not be rendered>"
		}
	}()
	switch x := v.(type) {
	case nil:
		return "<nil panic>"
	case runtime.Error:
		return x.Error()
	case error:
		return chain(x)
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprintf("%T: %v", v, v)
	}
}

// chain renders an error and its causes, with a depth limit and a seen set so a
// cyclic chain cannot hang the recovery, and a guard around each Error() call.
func chain(err error) string {
	seen := map[error]bool{}
	out := ""
	for depth := 0; err != nil && depth < maxChainDepth; depth++ {
		if seen[err] {
			break
		}
		func() {
			defer func() {
				if recover() != nil {
					out += "<error could not be rendered>"
					err = nil
				}
			}()
			seen[err] = true
			if out != "" {
				out += ": "
			}
			out += err.Error()
			err = errors.Unwrap(err)
		}()
	}
	return out
}
