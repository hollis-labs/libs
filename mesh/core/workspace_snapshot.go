package mesh

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode"
)

const (
	WorkspaceSnapshotTakenKind       = "workspace.snapshot.taken"
	WorkspaceSnapshotPayloadV1       = "workspace.snapshot.taken.v1"
	MaxWorkspaceSnapshotPayloadBytes = 64 * 1024
)

// SnapshotObservationInterval bounds a disk observation, not an atomic cut.
type SnapshotObservationInterval struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// SnapshotJournalInterval uses the authenticated source journal's positions:
// After is exclusive and Through inclusive. Zero is a genuine initial position,
// never a substitute for an unavailable journal. These are NOT Event.Cursor.
type SnapshotJournalInterval struct {
	JournalID string `json:"journal_id"`
	After     uint64 `json:"after"`
	Through   uint64 `json:"through"`
}

type SnapshotSkipped struct {
	Reason string `json:"reason"`
	Count  uint64 `json:"count"`
}

type SnapshotRepositoryObservation struct {
	Head       string    `json:"head"`
	Branch     string    `json:"branch,omitempty"`
	Detached   bool      `json:"detached"`
	ObservedAt time.Time `json:"observed_at"`
}

// SnapshotRootOutcome contains logical identities, never root paths, file names,
// content or provider error text. Identity redaction/custody belongs to the host;
// structurally valid data is not proof that arbitrary strings contain no secrets.
type SnapshotRootOutcome struct {
	RootID      string                         `json:"root_id"`
	StoreID     string                         `json:"store_id"`
	Observation SnapshotObservationInterval    `json:"observation"`
	TreeHash    string                         `json:"tree_hash,omitempty"`
	CommitHash  string                         `json:"commit_hash,omitempty"`
	Repository  *SnapshotRepositoryObservation `json:"repository,omitempty"`
	Skipped     []SnapshotSkipped              `json:"skipped"`
	ErrorCode   string                         `json:"error_code,omitempty"`
}

// WorkspaceSnapshotTaken is a recorded observation contract. It confers no
// capture/restore authority, pins, custody, quiescence or launch readiness.
// Hosts publish only AFTER durable capture and retention recording. Failed roots
// remain explicit; no synthetic cross-root tree or per-tool history is implied.
type WorkspaceSnapshotTaken struct {
	SchemaVersion     string                      `json:"schema_version"`
	SetID             string                      `json:"set_id"`
	OperationID       string                      `json:"operation_id"`
	InputDigest       string                      `json:"input_digest"`
	TargetMapDigest   string                      `json:"target_map_digest"`
	PolicyRevision    string                      `json:"policy_revision"`
	RunID             string                      `json:"run_id"`
	InstanceID        string                      `json:"instance_id"`
	Observation       SnapshotObservationInterval `json:"observation"`
	Journal           SnapshotJournalInterval     `json:"journal"`
	BindingFence      string                      `json:"binding_fence"`
	ControllerEpoch   uint64                      `json:"controller_epoch"`
	BootGeneration    string                      `json:"boot_generation"`
	RuntimeGeneration string                      `json:"runtime_generation"`
	Reason            string                      `json:"reason"`
	Boundary          string                      `json:"boundary"`
	Complete          bool                        `json:"complete"`
	Coalesced         bool                        `json:"coalesced"`
	Uncaptured        *SnapshotJournalInterval    `json:"uncaptured,omitempty"`
	Roots             []SnapshotRootOutcome       `json:"roots"`
}

func snapshotInvalid() error { return NewError(ErrorInvalid, "invalid workspace snapshot payload") }
func snapshotID(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func snapshotHash(s string, bytes int) bool {
	if len(s) != bytes*2 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func snapshotGitHash(s string) bool { return snapshotHash(s, 20) || snapshotHash(s, 32) }
func (i SnapshotObservationInterval) valid() bool {
	return !i.StartedAt.IsZero() && !i.FinishedAt.IsZero() && !i.FinishedAt.Before(i.StartedAt)
}
func (i SnapshotObservationInterval) contains(t time.Time) bool {
	return !t.Before(i.StartedAt) && !t.After(i.FinishedAt)
}
func (i SnapshotJournalInterval) valid() bool { return snapshotID(i.JournalID) && i.Through >= i.After }

func (p WorkspaceSnapshotTaken) Validate() error {
	if p.SchemaVersion != WorkspaceSnapshotPayloadV1 || !snapshotID(p.SetID) || !snapshotID(p.OperationID) || !snapshotHash(p.InputDigest, 32) || !snapshotHash(p.TargetMapDigest, 32) || !snapshotID(p.PolicyRevision) || !snapshotID(p.RunID) || !snapshotID(p.InstanceID) || !p.Observation.valid() || !p.Journal.valid() || !snapshotID(p.BindingFence) || p.ControllerEpoch == 0 || !snapshotID(p.BootGeneration) || !snapshotID(p.RuntimeGeneration) || len(p.Roots) == 0 || len(p.Roots) > 128 {
		return snapshotInvalid()
	}
	switch p.Reason {
	case "run_start", "run_end", "pre_tool", "reattach", "manual":
	default:
		return snapshotInvalid()
	}
	if p.Boundary != "quiescent" && p.Boundary != "best_effort" {
		return snapshotInvalid()
	}
	if p.Coalesced {
		if p.Reason != "reattach" || p.Uncaptured == nil || !p.Uncaptured.valid() || p.Uncaptured.JournalID != p.Journal.JournalID || p.Uncaptured.Through > p.Journal.Through || p.Uncaptured.After < p.Journal.After {
			return snapshotInvalid()
		}
	} else if p.Uncaptured != nil || p.Reason == "reattach" {
		return snapshotInvalid()
	}
	if p.Complete && p.Boundary != "quiescent" {
		return snapshotInvalid()
	}
	roots := make(map[string]bool)
	for _, r := range p.Roots {
		if !snapshotID(r.RootID) || !snapshotID(r.StoreID) || roots[r.RootID] || !r.Observation.valid() || !p.Observation.contains(r.Observation.StartedAt) || !p.Observation.contains(r.Observation.FinishedAt) || len(r.Skipped) > 16 {
			return snapshotInvalid()
		}
		roots[r.RootID] = true
		switch r.ErrorCode {
		case "":
			if !snapshotGitHash(r.TreeHash) || !snapshotGitHash(r.CommitHash) {
				return snapshotInvalid()
			}
		case "capture_failed", "store_unavailable", "coverage_unavailable", "deferred":
			if p.Complete || r.TreeHash != "" || r.CommitHash != "" {
				return snapshotInvalid()
			}
		default:
			return snapshotInvalid()
		}
		reasons := make(map[string]bool)
		for _, s := range r.Skipped {
			if s.Count == 0 || reasons[s.Reason] {
				return snapshotInvalid()
			}
			reasons[s.Reason] = true
			switch s.Reason {
			case "excluded", "git_metadata":
			case "oversize_untracked", "unsupported", "unobserved":
				if p.Complete {
					return snapshotInvalid()
				}
			default:
				return snapshotInvalid()
			}
		}
		if r.Repository != nil {
			repo := r.Repository
			if !snapshotGitHash(repo.Head) || !r.Observation.contains(repo.ObservedAt) || repo.Detached == (repo.Branch != "") || len(repo.Branch) > 256 {
				return snapshotInvalid()
			}
			for _, c := range repo.Branch {
				if unicode.IsControl(c) {
					return snapshotInvalid()
				}
			}
		}
	}
	return nil
}

// DecodeWorkspaceSnapshotTaken refuses unknown versions/fields, duplicate or
// case-aliased keys, numeric overflow and trailing data. Future additions need a
// new payload version; do not silently reinterpret this closed v1 contract.
func DecodeWorkspaceSnapshotTaken(raw []byte) (WorkspaceSnapshotTaken, error) {
	var p WorkspaceSnapshotTaken
	if len(raw) == 0 || len(raw) > MaxWorkspaceSnapshotPayloadBytes {
		return p, snapshotInvalid()
	}
	keys := json.NewDecoder(bytes.NewReader(raw))
	keys.UseNumber()
	if err := snapshotJSONValue(keys, 0); err != nil {
		return p, snapshotInvalid()
	}
	if _, err := keys.Token(); err != io.EOF {
		return p, snapshotInvalid()
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return WorkspaceSnapshotTaken{}, snapshotInvalid()
	}
	if err := p.Validate(); err != nil {
		return WorkspaceSnapshotTaken{}, err
	}
	return p, nil
}

func snapshotJSONValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return snapshotInvalid()
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || keys[key] || strings.ToLower(key) != key {
				return snapshotInvalid()
			}
			keys[key] = true
			if err = snapshotJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		token, err = d.Token()
		if err != nil || token != json.Delim('}') {
			return snapshotInvalid()
		}
	case '[':
		for d.More() {
			if err = snapshotJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		token, err = d.Token()
		if err != nil || token != json.Delim(']') {
			return snapshotInvalid()
		}
	default:
		return snapshotInvalid()
	}
	return nil
}

// ValidateWorkspaceSnapshotEvent checks envelope/payload binding without
// interpreting destination Cursor as a source journal position. Persistence and
// continuous host authority are enforced by the issuer, not this pure validator.
func ValidateWorkspaceSnapshotEvent(e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Kind != WorkspaceSnapshotTakenKind || e.PayloadSchema != WorkspaceSnapshotPayloadV1 || e.ContentType != "application/json" || e.Visibility != "private" || e.Truncated {
		return snapshotInvalid()
	}
	p, err := DecodeWorkspaceSnapshotTaken(e.Payload)
	if err != nil {
		return err
	}
	if e.Time.Before(p.Observation.FinishedAt) || e.SessionID != p.RunID || e.CorrelationID != p.SetID || e.CausationID != p.OperationID {
		return snapshotInvalid()
	}
	return nil
}
