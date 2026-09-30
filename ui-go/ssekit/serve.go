package ssekit

import (
	"context"
	"errors"
	"io"
	"time"
)

// ServeOption configures Serve.
type ServeOption func(*serveConfig)

type serveConfig struct {
	heartbeat   time.Duration
	text        string
	maxLifetime time.Duration
	terminal    func(Event) bool
	onSrcErr    func(error) (Event, bool)
}

// WithHeartbeat sets how long the stream may be quiet before Serve writes a
// keepalive comment. The default is 15 seconds; d <= 0 turns heartbeats off.
func WithHeartbeat(d time.Duration) ServeOption {
	return func(c *serveConfig) { c.heartbeat = d }
}

// WithHeartbeatText sets the keepalive comment text (default "keepalive").
func WithHeartbeatText(s string) ServeOption {
	return func(c *serveConfig) { c.text = s }
}

// WithMaxLifetime ends the stream, cleanly, after d. Set it below the server's
// write timeout when that cannot be cleared; clients reconnect with their
// cursor. Zero means no limit.
func WithMaxLifetime(d time.Duration) ServeOption {
	return func(c *serveConfig) { c.maxLifetime = d }
}

// WithTerminal marks events after which the stream ends: the event is
// delivered, then Serve returns nil.
func WithTerminal(f func(Event) bool) ServeOption {
	return func(c *serveConfig) { c.terminal = f }
}

// WithOnSourceError lets the application write a last in-band frame when the
// source fails: if f returns true its event is sent (best effort) before Serve
// returns the source error.
func WithOnSourceError(f func(error) (Event, bool)) ServeOption {
	return func(c *serveConfig) { c.onSrcErr = f }
}

// Serve copies events from src to w until the stream ends, writing a keepalive
// comment whenever it has been quiet for the heartbeat interval.
//
// It returns nil when the source ends (io.EOF), a terminal event has been
// delivered, or the maximum lifetime is reached. It returns ctx's error when ctx
// ends first (typically the client going away), the error of a failed write or
// flush so the caller can abort upstream work, or the source's error. Serve
// does not return until the goroutine it uses to wait on src has exited, so
// src.Next must honor its context.
func Serve(ctx context.Context, w *Writer, src Source, o ...ServeOption) error {
	cfg := serveConfig{heartbeat: 15 * time.Second, text: "keepalive"}
	for _, f := range o {
		f(&cfg)
	}

	type result struct {
		ev  Event
		err error
	}
	sctx, cancel := context.WithCancel(ctx)
	results := make(chan result)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			ev, err := src.Next(sctx)
			select {
			case results <- result{ev, err}:
			case <-sctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		<-done
	}()

	var hbC, lifeC <-chan time.Time
	var hb *time.Timer
	if cfg.heartbeat > 0 {
		hb = time.NewTimer(cfg.heartbeat)
		defer hb.Stop()
		hbC = hb.C
	}
	if cfg.maxLifetime > 0 {
		life := time.NewTimer(cfg.maxLifetime)
		defer life.Stop()
		lifeC = life.C
	}
	quiet := func() {
		if hb != nil {
			hb.Reset(cfg.heartbeat)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-lifeC:
			return nil
		case <-hbC:
			if err := w.Comment(cfg.text); err != nil {
				return err
			}
			quiet()
		case r := <-results:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if cfg.onSrcErr != nil {
					if ev, ok := cfg.onSrcErr(r.err); ok {
						_ = w.Send(ev)
					}
				}
				return r.err
			}
			if err := w.Send(r.ev); err != nil {
				return err
			}
			quiet()
			if cfg.terminal != nil && cfg.terminal(r.ev) {
				return nil
			}
		}
	}
}
