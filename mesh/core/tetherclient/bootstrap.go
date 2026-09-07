package tether

// bootstrap.go — the provider-neutral local bootstrap/registration helper
// the messaging-vnext architecture calls for: "Add a provider-neutral
// local bootstrap/registration helper that agent-setup can invoke at the
// launch/host boundary; accept preassigned SESSION and idempotent
// fallback without requiring online Tether or editing Cairn/Tachyon."
//
// ResolveSessionBootstrap is the entry point. It ALWAYS resolves and
// returns a usable canonical session identity, even when no Tether
// daemon is reachable at all -- "Bootstrap and local messaging cannot
// require online Tether." Registering that identity with a reachable
// daemon is a best-effort second step (Result.Registered reports
// whether it actually happened); a caller that only needs the resolved
// SessionID (e.g. to inject SESSION=<id> into a child process's
// environment) never has to check whether Tether is running at all.
//
// Idempotent by construction: calling this again with the SAME
// preassigned SessionID (via opts.SessionID or the CanonicalSessionEnvKey
// env var) always resolves to that same ID, and the daemon-side endpoint
// (POST /sessions/bootstrap) never invents a competing identity for an
// ID it has already seen -- "A second hook invocation must not invent a
// competing identity."

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/google/uuid"
)

// CanonicalSessionEnvKey is the environment variable a launcher/host may
// preassign before starting a session, and that a session/tool later
// reads to discover its own canonical identity. Matches Tether's own
// "SESSION" convention.
const CanonicalSessionEnvKey = "SESSION"

// BootstrapIntent mirrors Tether's canonical session-identity intent
// vocabulary.
type BootstrapIntent string

const (
	IntentPreassigned BootstrapIntent = "preassigned"
	IntentResume      BootstrapIntent = "resume"
	IntentCompact     BootstrapIntent = "compact"
	IntentFresh       BootstrapIntent = "fresh"
	IntentFork        BootstrapIntent = "fork"
)

// PublicationChoice mirrors Tether's canonical publication vocabulary.
type PublicationChoice string

const (
	PublicationPrivateLocal   PublicationChoice = "private-local"
	PublicationPublishedLocal PublicationChoice = "published-local"
	PublicationTetherHosted   PublicationChoice = "tether-hosted"
)

// ProviderMapping is one provider-native session id to record against
// the canonical session (e.g. Owner="tether", Provider="claude-code",
// NativeSessionID=<the provider's own session id>). Omit it entirely
// when the native id isn't known yet -- bootstrap succeeds either way;
// a later reconnect call can supply it once it becomes known.
type ProviderMapping struct {
	Owner           string `json:"owner"`
	Provider        string `json:"provider"`
	NativeSessionID string `json:"native_session_id"`
}

// BootstrapOptions configures ResolveSessionBootstrap. Only SessionID
// participates in local (offline) resolution; every other field is
// forwarded to the daemon only if/when it's reachable.
type BootstrapOptions struct {
	// SessionID, when non-empty, is used verbatim (the "preassigned"
	// case: the launcher already decided the canonical id, e.g. by
	// generating it itself before this call). When empty,
	// ResolveSessionBootstrap reads the CanonicalSessionEnvKey
	// environment variable; when that's also empty, it mints a fresh
	// random session id (uuid v4) and IntentFresh applies unless
	// overridden.
	SessionID       string
	Intent          BootstrapIntent
	ParentSessionID string
	// LogicalAgentID, when set, associates this session with a durable
	// actor rather than a one-off session.
	LogicalAgentID   string
	Publication      PublicationChoice
	ProviderMappings []ProviderMapping
}

// BootstrapResult is the resolved local identity, plus whether the
// best-effort daemon registration actually happened.
type BootstrapResult struct {
	// SessionID is always populated, regardless of Tether's
	// reachability -- resolve this and use it (e.g. inject it as the
	// child process's SESSION env var) before even checking Registered.
	SessionID string
	Intent    BootstrapIntent
	// Registered is true only when the daemon call succeeded. False
	// covers both "Tether unreachable" (RegisterErr wraps
	// ErrDaemonUnreachable) and any other daemon-side error
	// (RegisterErr holds it) -- callers that don't care why can just
	// check Registered.
	Registered bool
	// RegisterErr is nil when Registered is true, and non-nil
	// otherwise, naming why registration didn't happen. It is
	// deliberately NOT returned as ResolveSessionBootstrap's own error
	// return -- a bootstrap call that resolves an identity but can't
	// reach Tether is still a successful bootstrap for the "without
	// requiring online Tether" contract.
	RegisterErr error
}

// ResolveSessionBootstrap resolves a session's canonical identity
// locally (see BootstrapOptions.SessionID for the resolution order) and
// best-effort registers it with c via POST /sessions/bootstrap. c may be
// nil -- registration is then always skipped, exactly like an
// unreachable daemon. The returned error is non-nil ONLY for a caller
// mistake (e.g. an invalid explicit SessionID); a reachability or
// daemon-side registration failure surfaces via
// BootstrapResult.RegisterErr instead, never here.
func ResolveSessionBootstrap(ctx context.Context, c *Client, opts BootstrapOptions) (BootstrapResult, error) {
	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = os.Getenv(CanonicalSessionEnvKey)
	}
	intent := opts.Intent
	if sessionID == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			return BootstrapResult{}, fmt.Errorf("tether: resolve session bootstrap: mint session id: %w", err)
		}
		sessionID = id.String()
		if intent == "" {
			intent = IntentFresh
		}
	}
	if intent == "" {
		intent = IntentPreassigned
	}

	result := BootstrapResult{SessionID: sessionID, Intent: intent}

	if c == nil {
		result.RegisterErr = ErrDaemonUnreachable
		return result, nil
	}

	_, err := c.BootstrapSession(ctx, SessionBootstrapRequest{
		SessionID:        sessionID,
		Intent:           string(intent),
		ParentSessionID:  opts.ParentSessionID,
		LogicalAgentID:   opts.LogicalAgentID,
		Publication:      string(opts.Publication),
		ProviderMappings: opts.ProviderMappings,
	})
	if err != nil {
		result.RegisterErr = err
		return result, nil
	}
	result.Registered = true
	return result, nil
}

// SessionBootstrapRequest is the wire payload for Client.BootstrapSession.
// SessionID is required; every other field is optional.
type SessionBootstrapRequest struct {
	SessionID        string            `json:"session_id"`
	Intent           string            `json:"intent,omitempty"`
	ParentSessionID  string            `json:"parent_session_id,omitempty"`
	LogicalAgentID   string            `json:"logical_agent_id,omitempty"`
	Publication      string            `json:"publication,omitempty"`
	ProviderMappings []ProviderMapping `json:"provider_mappings,omitempty"`
}

// SessionBootstrapResult is the daemon's response to BootstrapSession.
type SessionBootstrapResult struct {
	SessionID string `json:"session_id"`
	// Created is false when session_id already had a row on the daemon
	// -- the idempotent/repeated-call/reconnect case.
	Created bool `json:"created"`
}

// BootstrapSession POSTs /sessions/bootstrap directly. Most callers want
// ResolveSessionBootstrap instead (it also resolves the SessionID
// locally and never requires an online daemon); this is the lower-level
// wire call for a caller that has already decided the full payload.
func (c *Client) BootstrapSession(ctx context.Context, req SessionBootstrapRequest) (SessionBootstrapResult, error) {
	if req.SessionID == "" {
		return SessionBootstrapResult{}, fmt.Errorf("tether: bootstrap session: session_id is required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return SessionBootstrapResult{}, fmt.Errorf("tether: marshal bootstrap request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sessions/bootstrap", bytes.NewReader(body))
	if err != nil {
		return SessionBootstrapResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return SessionBootstrapResult{}, wrapIfUnreachable(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return SessionBootstrapResult{}, readAPIError(resp)
	}
	var out SessionBootstrapResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SessionBootstrapResult{}, fmt.Errorf("tether: decode bootstrap response: %w", err)
	}
	return out, nil
}
