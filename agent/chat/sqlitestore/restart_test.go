package sqlitestore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

func service(t *testing.T, path string) (*chat.Service, func()) {
	t.Helper()
	st := open(t, path)
	svc, err := chat.NewService(chat.Config{
		Log: st, Control: st, Runner: chattest.NewScripted(),
		Agents: func(context.Context, string) (chat.Agent, error) {
			return chat.Agent{Owner: "owner-1", Kind: "embedded"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := svc.Close(ctx); err != nil {
			t.Errorf("Service.Close: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Errorf("Store.Close: %v", err)
		}
	}
}

func readTo(t *testing.T, svc *chat.Service, conv, att string, from chat.Cursor, stop func(chatstream.Event) bool) []chatstream.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := svc.Events(ctx, conv, att, from)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var out []chatstream.Event
	for {
		ev, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, ev)
		if stop(ev) {
			return out
		}
	}
}

// TestConversationSurvivesARestart: the conversation, its log and every cursor
// are in the file, so a new process serves the same conversation from the same
// place.
func TestConversationSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat.db")

	svc, stop := service(t, path)
	v, err := svc.EnsureConversation(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	att, _, err := svc.Attach(ctx, v.ConversationID, chat.RoleController, chat.StreamDeltas)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.SubmitAs(ctx, v.ConversationID, chat.Submission{Attachment: att.ID, Text: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	first := readTo(t, svc, v.ConversationID, att.ID, chat.After(0), func(ev chatstream.Event) bool { return ev.IsTerminal() })
	before, err := svc.Conversation(ctx, v.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	stop()

	svc, stop = service(t, path)
	defer stop()
	after, err := svc.EnsureConversation(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.ConversationID != v.ConversationID || after.Head != before.Head || after.Generation != 1 || after.ActiveRun != nil {
		t.Fatalf("after a restart = %+v; before = %+v", after, before)
	}
	if after.Lease == nil || after.Lease.Attachment != att.ID {
		t.Fatalf("lease after a restart = %+v", after.Lease)
	}
	// The same attachment resumes from a cursor in the middle of the old run.
	mid := first[len(first)/2].Seq
	rest := readTo(t, svc, v.ConversationID, att.ID, chat.After(mid), func(ev chatstream.Event) bool { return ev.IsTerminal() })
	var want []chatstream.Event
	for _, ev := range first {
		if ev.Seq > mid && ev.Verb != chatstream.VerbPartDelta { // history comes without deltas
			want = append(want, ev)
		}
	}
	if len(rest) != len(want) {
		t.Fatalf("resumed %d events after seq %d, want %d", len(rest), mid, len(want))
	}
	for i := range want {
		if rest[i].Seq != want[i].Seq || rest[i].Verb != want[i].Verb || rest[i].RunID != receipt.RunID || string(rest[i].Final) != string(want[i].Final) {
			t.Fatalf("event %d after the restart = seq %d %s, want seq %d %s", i, rest[i].Seq, rest[i].Verb, want[i].Seq, want[i].Verb)
		}
	}
	// And the next turn continues the same numbering.
	next, err := svc.SubmitAs(ctx, v.ConversationID, chat.Submission{Attachment: att.ID, Text: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Cursor != before.Head {
		t.Fatalf("cursor of the first turn after a restart = %d, want %d", next.Cursor, before.Head)
	}
}
