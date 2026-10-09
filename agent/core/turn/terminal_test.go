package turn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/agent/turn"
	llmtypes "github.com/hollis-labs/substrate/llm-core/llmtypes"
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

func TestReaderCanceledAccountingDrainsOnlyAlreadyBufferedEvents(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("caller canceled")
	events := make(chan llmtypes.StreamEvent, 4)
	events <- llmtypes.StreamEvent{Type: llmtypes.EventUsage, Usage: &llmtypes.Usage{InputTokens: 12}}
	events <- llmtypes.StreamEvent{Type: llmtypes.EventError, Error: "incurred provider failure"}
	reader, err := turn.NewReaderWithOptions(ctx, events, time.Second, turn.ReaderOptions{DrainBufferedOnCancel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cancel(cause)
	event, ok, err := reader.Next()
	if !ok || err != nil || event.Usage == nil || event.Usage.InputTokens != 12 {
		t.Fatalf("usage lost: %+v %v %v", event, ok, err)
	}
	events <- llmtypes.StreamEvent{Type: llmtypes.EventDone} // after cancellation queue capture
	event, ok, err = reader.Next()
	if !ok || err != nil || event.Error != "incurred provider failure" {
		t.Fatalf("failure lost: %+v %v %v", event, ok, err)
	}
	_, ok, err = reader.Next()
	if ok || !errors.Is(err, cause) || reader.TerminalSeen() {
		t.Fatalf("unbounded drain or canceled success: %v %v", ok, err)
	}
}

func TestReaderActivityRenewsInactivityWindow(t *testing.T) {
	const window = 200 * time.Millisecond
	events := make(chan llmtypes.StreamEvent, 1)
	reader, err := turn.NewReader(t.Context(), events, window)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i := 0; i < 5; i++ {
		time.Sleep(window / 4)
		events <- llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "still active"}
		event, ok, err := reader.Next()
		if !ok || err != nil || event.Content != "still active" {
			t.Fatalf("active stream expired at %d: %v %v", i, ok, err)
		}
	}
	_, ok, err := reader.Next()
	if ok || !errors.Is(err, turn.ErrTurnStalled) {
		t.Fatalf("silent stream not stalled: %v %v", ok, err)
	}
}
