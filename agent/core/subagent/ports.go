package subagent

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"runtime/debug"
	"time"
)

// Database is the host-owned SQLite execution port. The service does not open
// databases, run application migrations, or seed profiles. See STORAGE.md for
// the required subagent_runs contract. *sql.DB satisfies this port.
type Database interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Settings contains only the host's subagent approval posture.
type Settings struct {
	SubagentApprovalRequired       bool
	SubagentApprovalTimeoutSeconds int
	DeveloperMode                  bool
}

// Profile is host-resolved executable identity; it is never a tool grant.
type Profile struct {
	ID         string
	Slug       string
	CanExecute bool
}

// SpawnAuthorization is an explicit host decision, made with host policy.
// A Refusal is returned unchanged so errors.Is preserves its typed identity.
// Neither request metadata nor the role's definition grants bypass authority.
type SpawnAuthorization struct {
	Refusal        error
	BypassApproval bool
}

var ErrAuthorizationUnavailable = errors.New("subagent: host spawn authorization unavailable")

func TimeoutInRange(seconds int) bool {
	return seconds >= MinTimeoutSeconds && seconds <= MaxTimeoutSeconds
}
func HeartbeatIntervalInRange(seconds int) bool {
	return seconds >= MinHeartbeatSeconds && seconds <= MaxHeartbeatSeconds
}

// SetLivenessResolvers installs caller-owned runtime configuration. Configure
// ports before any Spawn; the service never reads environment variables.
func (svc *Service) SetLivenessResolvers(timeout func() int, heartbeat func() time.Duration) {
	svc.timeout, svc.heartbeat = timeout, heartbeat
}

func (svc *Service) defaultTimeoutSeconds() int {
	if svc.timeout != nil {
		if value := svc.timeout(); value >= MinTimeoutSeconds && value <= MaxTimeoutSeconds {
			return value
		}
	}
	return DefaultTimeoutSeconds
}

func (svc *Service) heartbeatInterval() time.Duration {
	if svc.heartbeat != nil {
		value := svc.heartbeat()
		if value == 0 || (value >= MinHeartbeatSeconds*time.Second && value <= MaxHeartbeatSeconds*time.Second) {
			return value
		}
	}
	return DefaultHeartbeatSeconds * time.Second
}

// SetPanicReporter retains the host's tracing and panic observation. Configure
// it before starting workers. A recovered panic remains contained to its worker.
func (svc *Service) SetPanicReporter(reporter func(context.Context, string, any, []byte)) {
	svc.panicReporter = reporter
}

func (svc *Service) goSafe(ctx context.Context, label string, fn func()) {
	go func() {
		defer func() {
			if value := recover(); value != nil {
				stack := debug.Stack()
				if svc.panicReporter != nil {
					svc.panicReporter(ctx, label, value, stack)
				} else {
					slog.Error("subagent: recovered panic", "label", label, "panic", value, "stack", string(stack))
				}
			}
		}()
		fn()
	}()
}
