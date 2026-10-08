// Package service provides native per-run status, snapshot and replay over the
// shared chatstream contract. Preparation, policy, persistence and admission
// remain embedding host responsibilities.
package service

import (
	"context"
	"encoding/json"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	streamhub "github.com/hollis-labs/go-streamhub"
)

// SnapshotStore commits the entire snapshot, including canonical reduction and
// checkpoint, atomically. It returns sql.ErrNoRows for an unknown owned turn.
// Snapshots survive log expiry and restarts; event logs are process-local.
type SnapshotStore interface {
	GetCognitiveTurnSnapshot(context.Context, string, string) (string, error)
	SaveCognitiveTurnSnapshot(context.Context, string, string, string) error
}

// Options sets caller-owned activity names or a caller-supplied event hub.
// Activity namespaces carry provenance and do not confer any authority.
type Options struct {
	Hub               *streamhub.Hub
	StateActivityKind string
	ActivityPrefix    string
}

// Committed carries opaque host output and its display projection. Loading it
// happens after the host has committed final/partial output. A loader error
// prevents reporting successful completion. Content nil retains streamed text.
type Committed[Message any] struct {
	Message     *Message
	Content     *string
	Interrupted bool
}

// Input is the neutral projection of a host producer event. Raw preserves the
// host's activity payload without importing its product event DTO.
type Input struct {
	Type, Content, Phase, Tool, ToolID, Detail, Summary, Data, Error string
	IsError                                                          bool
	Failure                                                          *chatstream.RunError
	Raw                                                              json.RawMessage
}

// ApprovalPrompt is a once-bound permission descriptor. Responding remains in
// the shared approval registry; this projection never broadens a host grant.
type ApprovalPrompt struct {
	RequestID string         `json:"request_id"`
	CallID    string         `json:"call_id"`
	Tool      string         `json:"tool"`
	Input     map[string]any `json:"input"`
	Reason    string         `json:"reason"`
	ExpiresAt time.Time      `json:"expires_at"`
}
