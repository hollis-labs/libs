package agentdir

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/transcript"
)

// TranscriptStableStore is a durable single-writer Store for stable agents.
// The host chooses its private root and authenticates requests before calling
// it. Task closure and embedded app records still require a host-specific Store.
type TranscriptStableStore struct{ Root string }

type transcriptLease struct {
	writer *transcript.Writer
	id     string
	ctx    context.Context
	once   sync.Once
}

func (l *transcriptLease) ConversationID() string   { return l.id }
func (l *transcriptLease) Context() context.Context { return l.ctx }
func (l *transcriptLease) Release()                 { l.once.Do(func() { _ = l.writer.Close() }) }

func stableID(s Snapshot) (string, error) {
	if s.Profile.Kind != Stable || s.Profile.SeatID == "" {
		return "", fmt.Errorf("%w: transcript store supports stable seats only", ErrProfile)
	}
	// A deterministic ID selects the same conversation after process restart.
	// The definition ID is checked against an identity row instead of silently
	// selecting a new conversation when a file changes.
	sum := sha256.Sum256([]byte("agentdir/stable/v1\x00" + s.Profile.SeatID))
	return "stable_" + hex.EncodeToString(sum[:]), nil
}

type identity struct {
	SeatID       string `json:"seat_id"`
	DefinitionID string `json:"definition_id"`
}

func (s TranscriptStableStore) Acquire(ctx context.Context, snap Snapshot) (Lease, error) {
	if err := snap.Profile.Validate(&snap.Definition); err != nil {
		return nil, err
	}
	id, err := stableID(snap)
	if err != nil {
		return nil, err
	}
	writer, err := transcript.Open(s.Root, id, transcript.Options{})
	if errors.Is(err, transcript.ErrBusy) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = writer.Close()
		}
	}()
	entries, err := transcript.Replay(s.Root, id, transcript.Cursor{})
	if err != nil {
		return nil, err
	}
	want := identity{SeatID: snap.Profile.SeatID, DefinitionID: snap.Definition.DefinitionID}
	if len(entries) == 0 {
		payload, _ := json.Marshal(want)
		record, err := transcript.EncodeSideband(transcript.SidebandRecord{Type: "system", Subtype: "agentdir.identity", UUID: "identity", SessionID: id, Timestamp: time.Now().UTC(), Payload: payload})
		if err != nil {
			return nil, err
		}
		if _, err := writer.Append(record); err != nil {
			return nil, err
		}
	} else {
		var first struct {
			Type    string   `json:"type"`
			Subtype string   `json:"subtype"`
			Payload identity `json:"payload"`
		}
		if err := json.Unmarshal(entries[0].Record, &first); err != nil || first.Type != "system" || first.Subtype != "agentdir.identity" || first.Payload != want {
			return nil, fmt.Errorf("%w: stable seat definition changed", ErrProfile)
		}
	}
	closeOnError = false
	return &transcriptLease{writer: writer, id: id, ctx: ctx}, nil
}

func (s TranscriptStableStore) EndTask(context.Context, string) error {
	return fmt.Errorf("%w: transcript store supports stable seats only", ErrProfile)
}

// Replay reads the verified committed suffix after cursor. A caller can keep
// the cursor across host processes; transcript.Replay detects missing or
// changed history and returns the verified prefix with ErrTorn for a torn tail.
func (s TranscriptStableStore) Replay(snap Snapshot, after transcript.Cursor) ([]transcript.Entry, error) {
	id, err := stableID(snap)
	if err != nil {
		return nil, err
	}
	return transcript.Replay(s.Root, id, after)
}

func (l *transcriptLease) Commit(input string, result sdk.Result) error {
	if result.Status != sdk.Succeeded {
		return fmt.Errorf("agentdir: only completed turns may be committed")
	}
	userID := rand.Text()
	userBody, _ := json.Marshal(map[string]any{"role": "user", "content": input})
	user, err := transcript.EncodeMessage(transcript.MessageRecord{Type: "user", UUID: userID, SessionID: l.id, Timestamp: time.Now().UTC(), Message: userBody})
	if err != nil {
		return err
	}
	if _, err := l.writer.Append(user); err != nil {
		return err
	}
	answerBody, _ := json.Marshal(map[string]any{"role": "assistant", "content": result.Text})
	answer, err := transcript.EncodeMessage(transcript.MessageRecord{Type: "assistant", UUID: rand.Text(), ParentUUID: &userID, SessionID: l.id, Timestamp: time.Now().UTC(), Message: answerBody})
	if err != nil {
		return err
	}
	_, err = l.writer.Append(answer)
	return err
}
