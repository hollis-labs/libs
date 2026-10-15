package subagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	messaging "github.com/hollis-labs/substrate/mesh/messaging/mailbox"
)

var errTestUntrusted = errors.New("host: role is untrusted")

type permitTestSpawn struct{}

func (permitTestSpawn) AuthorizeSpawn(context.Context, string) (SpawnAuthorization, error) {
	return SpawnAuthorization{}, nil
}
func newTestService(db Database, runner Runner, poster MessagePoster, approver ApprovalEmitter, settings SettingsReader) *Service {
	svc := NewService(db, runner, poster, approver, settings)
	svc.SetSpawnAuthorizer(permitTestSpawn{})
	svc.SetLivenessResolvers(resolveDefaultTimeoutSeconds, resolveHeartbeatInterval)
	return svc
}

type testProfiles struct{ profiles map[string]*Profile }

func (p *testProfiles) CreateAgent(_ context.Context, profile *Profile) error {
	p.profiles[profile.Slug] = profile
	return nil
}
func (p *testProfiles) GetAgentBySlug(_ context.Context, slug string) (*Profile, error) {
	if v := p.profiles[slug]; v != nil {
		return v, nil
	}
	return nil, sql.ErrNoRows
}

type profilePoster struct {
	mu       sync.Mutex
	profiles *testProfiles
	messages []*messaging.Message
}

func newProfilePoster(p *testProfiles) *profilePoster { return &profilePoster{profiles: p} }
func (p *profilePoster) SendMessage(_ context.Context, input messaging.SendInput) (*messaging.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	found := false
	for _, profile := range p.profiles.profiles {
		if profile.ID == input.ToAgentID {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown recipient %s", input.ToAgentID)
	}
	msg := &messaging.Message{ID: "reply", ToSessionID: input.ToSessionID, Kind: input.Kind, ToAgentID: input.ToAgentID, FromAgentID: input.FromAgentID, Body: input.Body}
	p.messages = append(p.messages, msg)
	return msg, nil
}
func (p *profilePoster) RecentForSession(_ context.Context, session string, _ int) ([]*messaging.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result []*messaging.Message
	for _, message := range p.messages {
		if message.ToSessionID == session {
			result = append(result, message)
		}
	}
	return result, nil
}

// defaultTimeoutEnvVar is the operator knob for the wall-clock backstop
// budget applied to a subagent run that does not carry an explicit
// per-call timeout. Mirrors Torque's profile/env tiering. A value
// outside [MinTimeoutSeconds, MaxTimeoutSeconds] is ignored with a
// warning so a typo can't silently disable the backstop.
const defaultTimeoutEnvVar = "NANITE_SUBAGENT_DEFAULT_TIMEOUT_SECONDS"

// timeoutInRange reports whether secs is a usable subagent-run timeout.

// resolveDefaultTimeoutSeconds picks the wall-clock backstop budget for
// a subagent run that did not supply an explicit per-call timeout, in
// priority order (mirrors Torque resolveTimeout, timeout.go:35-43):
//
//  1. NANITE_SUBAGENT_DEFAULT_TIMEOUT_SECONDS when set and within
//     [MinTimeoutSeconds, MaxTimeoutSeconds]
//  2. DefaultTimeoutSeconds (the compiled-in 1800s floor)
//
// An explicit, in-range req.TimeoutSeconds still takes precedence over
// both — that check stays in Spawn, ahead of this call. An env value
// that is unparseable or out of range is ignored (with a warning) so a
// misconfiguration falls back safely rather than disabling the backstop.
func resolveDefaultTimeoutSeconds() int {
	raw := strings.TrimSpace(os.Getenv(defaultTimeoutEnvVar))
	if raw == "" {
		return DefaultTimeoutSeconds
	}
	secs, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("subagent: ignoring non-integer timeout override env var",
			"env", defaultTimeoutEnvVar, "value", raw, "fallback_seconds", DefaultTimeoutSeconds)
		return DefaultTimeoutSeconds
	}
	if !TimeoutInRange(secs) {
		slog.Warn("subagent: ignoring out-of-range timeout override env var",
			"env", defaultTimeoutEnvVar, "value", secs,
			"min", MinTimeoutSeconds, "max", MaxTimeoutSeconds,
			"fallback_seconds", DefaultTimeoutSeconds)
		return DefaultTimeoutSeconds
	}
	return secs
}

// heartbeatSecondsEnvVar is the operator knob for the heartbeat cadence.
const heartbeatSecondsEnvVar = "NANITE_SUBAGENT_HEARTBEAT_SECONDS"

// heartbeatIntervalInRange reports whether secs is a usable heartbeat
// cadence (0 is handled separately by the caller as "disabled").

// resolveHeartbeatInterval picks the "still running" ping cadence for a
// subagent run, in priority order:
//
//  1. NANITE_SUBAGENT_HEARTBEAT_SECONDS == "0" → heartbeats disabled
//     (returns 0).
//  2. NANITE_SUBAGENT_HEARTBEAT_SECONDS set to another in-range value →
//     that value.
//  3. unset, unparseable, or out of range → DefaultHeartbeatSeconds.
func resolveHeartbeatInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(heartbeatSecondsEnvVar))
	if raw == "" {
		return DefaultHeartbeatSeconds * time.Second
	}
	secs, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("subagent: ignoring non-integer heartbeat override env var",
			"env", heartbeatSecondsEnvVar, "value", raw, "fallback_seconds", DefaultHeartbeatSeconds)
		return DefaultHeartbeatSeconds * time.Second
	}
	if secs == 0 {
		return 0
	}
	if !HeartbeatIntervalInRange(secs) {
		slog.Warn("subagent: ignoring out-of-range heartbeat override env var",
			"env", heartbeatSecondsEnvVar, "value", secs,
			"min", MinHeartbeatSeconds, "max", MaxHeartbeatSeconds,
			"fallback_seconds", DefaultHeartbeatSeconds)
		return DefaultHeartbeatSeconds * time.Second
	}
	return time.Duration(secs) * time.Second
}
