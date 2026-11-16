package transcript_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
	"github.com/hollis-labs/libs/agent/sdk/transcript"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// This exercises the actual SDK fake model/tool loop with a host sink. The
// sink adds native Claude message wrappers and host sidebands. The synthetic
// tool result is known to this fake host; production hosts must retain the
// full result envelope and its immutable reference independently.
func TestActualSDKLoopWritesClaudeShape(t *testing.T) {
	h := fakehost.New()
	root := t.TempDir()
	conversation := string(h.Binding.ConversationID)
	w, err := transcript.Open(root, conversation, transcript.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	sequence := 0
	receipts := map[string][]byte{}
	var parent *string
	appendMessage := func(role string, content any, usage any, resultRef *runtimecontract.Reference) error {
		sequence++
		id := fmt.Sprintf("fixture-%d", sequence)
		body := map[string]any{"role": role, "content": content}
		if usage != nil {
			body["usage"] = usage
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		row, err := transcript.EncodeMessage(transcript.MessageRecord{Type: role, UUID: id, ParentUUID: parent, SessionID: conversation, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Message: raw, ResultRef: resultRef})
		if err != nil {
			return err
		}
		if _, err = w.Append(row); err != nil {
			return err
		}
		parent = &id
		return nil
	}
	if err := appendMessage("user", []any{map[string]any{"type": "text", "text": h.Input().Text}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	ports := h.Ports()
	ports.Events = runtimeevents.SinkFunc(func(_ context.Context, event runtimeevents.Event) error {
		var payload sdk.EventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		sequence++
		id := fmt.Sprintf("fixture-%d", sequence)
		sideband, err := transcript.EncodeSideband(transcript.SidebandRecord{Type: "system", Subtype: string(event.Kind), UUID: id, ParentUUID: parent, SessionID: conversation, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Payload: event.Payload})
		if err != nil {
			return err
		}
		if _, err = w.Append(sideband); err != nil {
			return err
		}
		parent = &id
		switch event.Kind {
		case runtimeevents.KindAgentToolUse:
			return appendMessage("assistant", []any{map[string]any{"type": "tool_use", "id": string(payload.ToolCallID), "name": payload.Tool, "input": map[string]any{}}}, nil, nil)
		case runtimeevents.KindAgentToolResult:
			// The fake host retains these synthetic envelope bytes. A real host
			// issues and verifies the reference against its durable receipt.
			envelope := []byte(`{"version":"resultenv/v1","outcome":"success"}`)
			sum := sha256.Sum256(envelope)
			ref := runtimecontract.Reference{ID: "fixture-receipt", Revision: "1", Digest: runtimecontract.Digest{Scheme: "sha256", Value: hex.EncodeToString(sum[:])}, IssuerRef: "fixture-host", OwnerID: h.Binding.OwnerID}
			receipts[ref.ID] = envelope
			return appendMessage("user", []any{map[string]any{"type": "tool_result", "tool_use_id": string(payload.ToolCallID), "content": "42", "is_error": false}}, nil, &ref)
		}
		return nil
	})
	op, err := sdk.New(fakehost.Definition(), ports)
	if err != nil {
		t.Fatal(err)
	}
	result, err := op.Run(context.Background(), h.Input())
	if err != nil {
		t.Fatal(err)
	}
	if err := appendMessage("assistant", []any{map[string]any{"type": "text", "text": result.Text}}, result.Usage, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := transcript.Replay(root, conversation, transcript.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	var calls, results, approvals, usage, references int
	for _, entry := range entries {
		var row struct {
			Type      string                     `json:"type"`
			Subtype   string                     `json:"subtype"`
			ResultRef *runtimecontract.Reference `json:"resultRef"`
			Message   struct {
				Role    string `json:"role"`
				Content []struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					ToolUseID string `json:"tool_use_id"`
				} `json:"content"`
				Usage json.RawMessage `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(entry.Record, &row); err != nil {
			t.Fatal(err)
		}
		if row.Type == "assistant" || row.Type == "user" {
			if row.Message.Role != row.Type {
				t.Fatal("Claude role mismatch")
			}
			for _, block := range row.Message.Content {
				if block.Type == "tool_use" && block.ID != "" {
					calls++
				}
				if block.Type == "tool_result" && block.ToolUseID != "" {
					results++
				}
			}
			if len(row.Message.Usage) > 0 {
				usage++
			}
			if row.ResultRef != nil && row.ResultRef.Validate() == nil {
				retained, ok := receipts[row.ResultRef.ID]
				if !ok {
					t.Fatal("missing retained receipt")
				}
				sum := sha256.Sum256(retained)
				if row.ResultRef.Digest.Value != hex.EncodeToString(sum[:]) {
					t.Fatal("receipt digest mismatch")
				}
				references++
			}
		}
		if row.Subtype == string(runtimeevents.KindAgentPermissionResolved) {
			approvals++
		}
	}
	if calls != 1 || results != 1 || approvals != 1 || usage != 1 || references != 1 || h.Effects != 1 || h.ModelCalls != 2 {
		t.Fatalf("ingestion fixture: calls=%d results=%d approvals=%d usage=%d refs=%d effects=%d models=%d", calls, results, approvals, usage, references, h.Effects, h.ModelCalls)
	}
}
