// Package runtimecontract carries experimental host-issued runtime and recovery
// references. Validation checks shape, never issuance, identity or permission.
// Hosts must resolve and verify references against current authoritative state.
package runtimecontract

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type OperativeID string
type SeatID string
type ConversationID string
type OwnerID string
type AttemptID string
type TurnID string
type ToolCallID string
type Generation uint64

// These positions have different producers and must never be substituted.
type ProducerSequence uint64
type ProducerGeneration uint64
type PTYByteOffset uint64
type CommittedCursor string

var (
	ErrInvalid     = errors.New("runtimecontract: invalid reference")
	ErrExpired     = errors.New("runtimecontract: expired reference")
	ErrUnsupported = errors.New("runtimecontract: unsupported capability")
)

// Binding identifies one host-issued attempt. Provider-native IDs are separate:
// they cannot establish ownership or substitute for Generation.
type Binding struct {
	OperativeID    OperativeID    `json:"operative_id"`
	SeatID         SeatID         `json:"seat_id,omitempty"`
	ConversationID ConversationID `json:"conversation_id"`
	OwnerID        OwnerID        `json:"owner_id"`
	AttemptID      AttemptID      `json:"attempt_id"`
	Generation     Generation     `json:"generation"`
}

func (b Binding) Validate() error {
	if strings.TrimSpace(string(b.OperativeID)) == "" || strings.TrimSpace(string(b.ConversationID)) == "" || strings.TrimSpace(string(b.OwnerID)) == "" || strings.TrimSpace(string(b.AttemptID)) == "" || b.Generation == 0 || (b.SeatID != "" && strings.TrimSpace(string(b.SeatID)) == "") {
		return fmt.Errorf("%w: operative, conversation, owner, attempt and positive generation required", ErrInvalid)
	}
	return nil
}

// Digest separates the hash algorithm from the canonical descriptor version.
// References use Scheme="sha256"; operation digests also require a nonblank
// DescriptorVersion. A display summary is not an operation descriptor.
type Digest struct {
	Scheme            string `json:"scheme"`
	DescriptorVersion string `json:"descriptor_version,omitempty"`
	Value             string `json:"value"`
}

func (d Digest) Validate() error {
	if d.Scheme != "sha256" || len(d.Value) != 64 || strings.ToLower(d.Value) != d.Value {
		return fmt.Errorf("%w: sha256 digest requires 64 lowercase hex characters", ErrInvalid)
	}
	if _, err := hex.DecodeString(d.Value); err != nil {
		return fmt.Errorf("%w: invalid digest hex", ErrInvalid)
	}
	return nil
}

// Reference is an immutable locator, not a credential or authentication proof.
// OwnerID scopes portable history and resources; hosts resolve access separately.
type Reference struct {
	ID        string  `json:"id"`
	Revision  string  `json:"revision"`
	Digest    Digest  `json:"digest"`
	IssuerRef string  `json:"issuer_ref"`
	OwnerID   OwnerID `json:"owner_id"`
}

func (r Reference) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Revision) == "" || strings.TrimSpace(r.IssuerRef) == "" || strings.TrimSpace(string(r.OwnerID)) == "" {
		return fmt.Errorf("%w: locator identity, revision, issuer and owner required", ErrInvalid)
	}
	return r.Digest.Validate()
}

// EnvironmentRef identifies already provisioned resources and an immutable
// grants/capability receipt. It conveys no provisioning or deployment authority.
type EnvironmentRef struct {
	Receipt      Reference `json:"receipt"`
	Workspace    Reference `json:"workspace"`
	Lease        Reference `json:"lease"`
	Grants       Reference `json:"grants"`
	Capabilities []string  `json:"capabilities"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (e EnvironmentRef) Validate() error {
	for _, r := range []Reference{e.Receipt, e.Workspace, e.Lease, e.Grants} {
		if err := r.Validate(); err != nil {
			return err
		}
		if r.OwnerID != e.Receipt.OwnerID {
			return fmt.Errorf("%w: environment owner mismatch", ErrInvalid)
		}
	}
	if e.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: environment expiry required", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, c := range e.Capabilities {
		if strings.TrimSpace(c) == "" || seen[c] {
			return fmt.Errorf("%w: empty or duplicate capability", ErrInvalid)
		}
		seen[c] = true
	}
	return nil
}

// ValidateAt checks receipt shape, declared capability coverage and deadline.
// A successful check is NOT host verification of current grants or lease state.
func (e EnvironmentRef) ValidateAt(now time.Time, required []string) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: admission time required", ErrInvalid)
	}
	if !now.Before(e.ExpiresAt) {
		return ErrExpired
	}
	for _, c := range required {
		if !slices.Contains(e.Capabilities, c) {
			return fmt.Errorf("%w: %s", ErrUnsupported, c)
		}
	}
	return nil
}

// RecoveryLocator carries portable references only. It never selects the newest
// conversation, transfers hidden provider context, or authorizes a restore.
// Optional references are pointers so absence differs from an incomplete locator.
type RecoveryLocator struct {
	Binding           Binding         `json:"binding"`
	Environment       EnvironmentRef  `json:"environment"`
	Transcript        Reference       `json:"transcript"`
	Workspace         Reference       `json:"workspace"`
	Plan              *Reference      `json:"plan,omitempty"`
	Snapshot          *Reference      `json:"snapshot,omitempty"`
	PendingOperations *Reference      `json:"pending_operations,omitempty"`
	CommittedCursor   CommittedCursor `json:"committed_cursor,omitempty"`
	LastCommittedTurn TurnID          `json:"last_committed_turn,omitempty"`
}

func (r RecoveryLocator) Validate() error {
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if err := r.Environment.Validate(); err != nil {
		return err
	}
	refs := []Reference{r.Transcript, r.Workspace, r.Environment.Receipt}
	for _, p := range []*Reference{r.Plan, r.Snapshot, r.PendingOperations} {
		if p != nil {
			refs = append(refs, *p)
		}
	}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return err
		}
		if ref.OwnerID != r.Binding.OwnerID {
			return fmt.Errorf("%w: recovery owner mismatch", ErrInvalid)
		}
	}
	if r.Workspace != r.Environment.Workspace {
		return fmt.Errorf("%w: recovery workspace differs from environment", ErrInvalid)
	}
	return nil
}
