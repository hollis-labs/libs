package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Switch is a Verifier whose backing verifier can be replaced while requests
// are being served, so verifier configuration (issuer, audience, JWKS URL or
// bootstrap key, deny-list source) can change without restarting the app. The
// zero value verifies nothing and answers ErrUnavailable until Swap installs a
// verifier. It is safe for concurrent use.
type Switch struct {
	v atomic.Pointer[verifierBox]
}

type verifierBox struct{ Verifier }

// NewSwitch returns a Switch holding v (which may be nil).
func NewSwitch(v Verifier) *Switch {
	s := &Switch{}
	s.Swap(v)
	return s
}

// Swap installs v and returns the previous verifier (nil if none). The caller
// owns stopping anything the old verifier was running (for example the context
// of its JWKS refresh loop).
func (s *Switch) Swap(v Verifier) Verifier {
	var nb *verifierBox
	if v != nil {
		nb = &verifierBox{v}
	}
	if old := s.v.Swap(nb); old != nil {
		return old.Verifier
	}
	return nil
}

// Verify implements Verifier.
func (s *Switch) Verify(ctx context.Context, token string) (Principal, error) {
	b := s.v.Load()
	if b == nil {
		return Principal{}, fmt.Errorf("%w: no verifier configured", ErrUnavailable)
	}
	return b.Verify(ctx, token)
}

// Reloader re-runs Load when the watched file's content changes, when the
// process receives SIGHUP, or when Trigger is called. It is the one reload
// mechanism for verifier and posture configuration: Load rebuilds what it
// needs and installs it (Switch.Swap, Guard.SetMode).
//
// Load must be all-or-nothing. If it returns an error the Reloader reports it
// and the previous configuration must still be in effect: a bad edit never
// takes a running app down. A file whose content failed to load is not retried
// until its content changes again (or a signal or Trigger forces it).
type Reloader struct {
	// Path is the file to watch. Empty means signal and Trigger only.
	Path string
	// Interval between checks of Path; default 2s. A change is applied after
	// it has been seen unchanged on two consecutive checks, so a half-written
	// file is not loaded.
	Interval time.Duration
	// Load applies the configuration.
	Load func(ctx context.Context) error
	// OnResult, optional, receives the outcome of every Load attempt.
	OnResult func(err error)
	// NoSignal disables the SIGHUP trigger.
	NoSignal bool
	Logger   *slog.Logger

	once    sync.Once
	trigger chan struct{}
}

func (r *Reloader) ensureTrigger() { r.once.Do(func() { r.trigger = make(chan struct{}, 1) }) }

// Trigger asks the running Reloader to run Load now. It never blocks.
func (r *Reloader) Trigger() {
	r.ensureTrigger()
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Run watches until ctx is done. It does not call Load for the file's initial
// content: the app loads once at startup and then hands over to Run.
func (r *Reloader) Run(ctx context.Context) error {
	if r.Load == nil {
		return errors.New("auth: Reloader.Load is required")
	}
	iv := r.Interval
	if iv <= 0 {
		iv = 2 * time.Second
	}
	lg := r.Logger
	if lg == nil {
		lg = slog.Default()
	}
	r.ensureTrigger()
	var sig chan os.Signal
	if !r.NoSignal {
		sig = make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP)
		defer signal.Stop(sig)
	}
	apply := func(reason string) {
		err := r.safeLoad(ctx)
		if err != nil {
			lg.Warn("auth: config reload failed; keeping previous configuration", "reason", reason, "err", err)
		} else {
			lg.Info("auth: config reloaded", "reason", reason)
		}
		if r.OnResult != nil {
			r.OnResult(err)
		}
	}
	applied, _ := fileDigest(r.Path) // what is in effect now (the startup load)
	var pending [sha256.Size]byte
	var havePending bool
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sig:
			apply("SIGHUP")
			applied, _ = fileDigest(r.Path)
			havePending = false
		case <-r.trigger:
			apply("trigger")
			applied, _ = fileDigest(r.Path)
			havePending = false
		case <-t.C:
			if r.Path == "" {
				continue
			}
			d, err := fileDigest(r.Path)
			if err != nil || d == applied {
				havePending = false // missing mid-replace, or unchanged
				continue
			}
			if !havePending || d != pending {
				pending, havePending = d, true // changed: confirm on the next check
				continue
			}
			havePending = false
			applied = d // record before Load so a failing file is not retried
			apply("file changed")
		}
	}
}

func (r *Reloader) safeLoad(ctx context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("auth: config load panicked: %v", p)
		}
	}()
	return r.Load(ctx)
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	if path == "" {
		return [sha256.Size]byte{}, errors.New("no path")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- operator-configured path
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(b), nil
}
