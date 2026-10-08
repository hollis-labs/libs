package turn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	llmtypes "github.com/hollis-labs/go-llm-types"
	"github.com/hollis-labs/substrate/agent/turn"
)

func TestCanonicalTurnRequiresProviderTerminal(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		events := make(chan llmtypes.StreamEvent, 3)
		events <- llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "partial"}
		events <- llmtypes.StreamEvent{Type: llmtypes.EventUsage, Usage: &llmtypes.Usage{StopReason: "end_turn"}}
		if terminal {
			events <- llmtypes.StreamEvent{Type: llmtypes.EventDone}
		}
		close(events)
		result, err := turn.ExecuteTurn(t.Context(), turn.TurnRequest{
			Stream:      func(context.Context) (<-chan llmtypes.StreamEvent, error) { return events, nil },
			IdleTimeout: time.Second, RequireTerminal: true,
		})
		if result.Text != "partial" {
			t.Fatalf("partial text lost: %+v", result)
		}
		if terminal && err != nil {
			t.Fatal(err)
		}
		if !terminal && !errors.Is(err, turn.ErrTurnTruncated) {
			t.Fatalf("EOF accepted as completion: %v", err)
		}
	}
}

func TestReaderCancellationWinsBufferedEventAndEOF(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		ctx, cancel := context.WithCancelCause(t.Context())
		cause := errors.New("caller canceled its turn")
		events := make(chan llmtypes.StreamEvent, 1)
		if buffered {
			events <- llmtypes.StreamEvent{Type: llmtypes.EventDone}
		}
		close(events)
		reader, err := turn.NewReader(ctx, events, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		cancel(cause)
		_, ok, err := reader.Next()
		if ok || !errors.Is(err, cause) || reader.TerminalSeen() {
			t.Fatalf("ok=%v terminal=%v err=%v", ok, reader.TerminalSeen(), err)
		}
	}
}
