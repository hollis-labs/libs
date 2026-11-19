// Package nativefallback offers a narrowly scoped backup nudge when the
// durable Tether mail path is delayed. Tether remains the mailbox of record.
package nativefallback

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var (
	ErrUnsupported = errors.New("nativefallback: provider or scope unsupported")
	ErrStaleRoute  = errors.New("nativefallback: stale target binding")
	ErrInvalid     = errors.New("nativefallback: invalid request")
)

// Capability describes the only native fallback currently backed by a CLI
// command: Codex queue to a named thread on the same local app-server host.
// Claude's team-scoped SendMessage and AGY's agent listing do not establish
// an addressable cross-session fallback, so they remain unsupported.
type Capability struct {
	Provider  string
	Supported bool
	Mechanism string
	Scope     string
	Reason    string
}

func Probe(provider string) Capability {
	switch strings.ToLower(provider) {
	case "codex":
		return Capability{Provider: provider, Supported: true, Mechanism: "codex queue --thread --message", Scope: "same local app-server host"}
	case "claude":
		return Capability{Provider: provider, Reason: "team-local SendMessage has no verified cross-session target route"}
	case "agy":
		return Capability{Provider: provider, Reason: "no verified native message-to-existing-session route"}
	default:
		return Capability{Provider: provider, Reason: "no provider-backed native route"}
	}
}

// Request names the durable mail item and the exact current Codex binding.
// The host must resolve and authorize this binding before calling Send.
type Request struct {
	Provider                  string
	TetherMessageID           string
	TargetThreadID            string
	TargetHostID              string
	CurrentHostID             string
	BindingGeneration         uint64
	ExpectedBindingGeneration uint64
}

// Receipt records only submission to the provider-native queue. It does not
// prove that the agent read, understood, or acted on the message.
type Receipt struct {
	TetherMessageID   string
	TargetThreadID    string
	BindingGeneration uint64
	Stage             string
}

// Runner invokes the already-installed Codex CLI. A host can inject a fake in
// tests; production callers provide ExecRunner with the installed binary path.
type Runner interface {
	Run(ctx context.Context, binary string, args ...string) error
}

// Authorizer checks that this caller may nudge this exact current binding.
// A native CLI command has no knowledge of Tether grants, so Send fails closed
// unless the owning host supplies this check.
type Authorizer interface {
	Authorize(ctx context.Context, request Request) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, binary string, args ...string) error {
	return exec.CommandContext(ctx, binary, args...).Run()
}

type Dispatcher struct {
	Binary     string
	Runner     Runner
	Authorizer Authorizer
}

func (d Dispatcher) Send(ctx context.Context, r Request) (Receipt, error) {
	if r.Provider != "codex" {
		return Receipt{}, fmt.Errorf("%w: %s", ErrUnsupported, r.Provider)
	}
	if !safeID(r.TetherMessageID) || r.TargetThreadID == "" || r.TargetHostID == "" || r.CurrentHostID == "" || d.Binary == "" || d.Runner == nil || d.Authorizer == nil {
		return Receipt{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if r.BindingGeneration == 0 || r.BindingGeneration != r.ExpectedBindingGeneration || r.TargetHostID != r.CurrentHostID {
		return Receipt{}, ErrStaleRoute
	}
	if err := d.Authorizer.Authorize(ctx, r); err != nil {
		return Receipt{}, fmt.Errorf("nativefallback: authorization: %w", err)
	}
	// The native channel carries only a pointer to the authoritative mail.
	// Replaying message content here would create a second mailbox truth.
	if err := d.Runner.Run(ctx, d.Binary, "queue", "--thread", r.TargetThreadID, "--message", "Check Tether message "+r.TetherMessageID); err != nil {
		return Receipt{}, fmt.Errorf("nativefallback: codex queue: %w", err)
	}
	return Receipt{TetherMessageID: r.TetherMessageID, TargetThreadID: r.TargetThreadID, BindingGeneration: r.BindingGeneration, Stage: "native_submitted"}, nil
}

func safeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// TetherState is the durable mail observation. A native submission never
// advances it. The host keeps retry/reconciliation against the Tether message
// ID until the authoritative mail path records delivery or consumption.
type TetherState string

const (
	TetherPending   TetherState = "pending"
	TetherDelivered TetherState = "delivered"
	TetherConsumed  TetherState = "consumed"
)

type Reconciliation struct {
	TetherMessageID string
	NativeSubmitted bool
	TetherState     TetherState
	Pending         bool
}

func Reconcile(receipt Receipt, state TetherState) (Reconciliation, error) {
	if receipt.TetherMessageID == "" || receipt.Stage != "native_submitted" {
		return Reconciliation{}, ErrInvalid
	}
	switch state {
	case TetherPending, TetherDelivered, TetherConsumed:
	default:
		return Reconciliation{}, ErrInvalid
	}
	return Reconciliation{TetherMessageID: receipt.TetherMessageID, NativeSubmitted: true, TetherState: state, Pending: state == TetherPending}, nil
}
