package chat

import (
	"encoding/json"
	"sort"
	"time"
)

// View is the state of a conversation as the wire reports it.
type View struct {
	ConversationID string `json:"conversation_id"`
	Agent          string `json:"agent"`
	Scope          string `json:"scope,omitempty"`
	Owner          string `json:"owner,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Generation     uint64 `json:"generation"`
	RunnerRef      string `json:"runner_ref,omitempty"`
	// Head is the cursor of the latest event.
	Head uint64 `json:"head"`
	// ActiveRun is the run in progress, or null.
	ActiveRun *string `json:"active_run"`
	// Lease is the unexpired input lease, or null.
	Lease            *LeaseView        `json:"lease"`
	Capabilities     Capabilities      `json:"capabilities"`
	PendingApprovals []PendingApproval `json:"pending_approvals,omitempty"`
}

// LeaseView is the input lease on the wire.
type LeaseView struct {
	Attachment string    `json:"attachment"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Capabilities says what a client can expect from this conversation.
type Capabilities struct {
	// Deltas is false when the runner yields whole messages only.
	Deltas bool `json:"deltas"`
	// Approvals is false when the runner never asks on this wire.
	Approvals bool `json:"approvals"`
	// Always is true when the host keeps standing grants, so an approval may
	// be answered with scope "always".
	Always bool `json:"always"`
}

// PendingApproval is an approval.request that has no answer yet.
type PendingApproval struct {
	ApprovalID string          `json:"approval_id"`
	RunID      string          `json:"run_id"`
	CallID     string          `json:"call_id,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Descriptor json.RawMessage `json:"descriptor,omitempty"`
	ExpiresAt  *time.Time      `json:"expires_at,omitempty"`
	// Seq is the cursor of the approval.request event.
	Seq uint64 `json:"seq"`
}

func sortApprovals(a []PendingApproval) {
	sort.Slice(a, func(i, j int) bool { return a[i].Seq < a[j].Seq })
}
