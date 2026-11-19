// Command chatserver serves the chat wire on localhost with the scripted
// runner, so the wire can be exercised with curl, or a client built against
// it, before any real runner exists. See the module README for the curl
// sequence.
//
//	go run ./examples/chatserver                 # in memory
//	go run ./examples/chatserver -db chat.db     # conversations survive a restart
//	go run ./examples/chatserver -whole          # a runner that does not stream
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	"github.com/hollis-labs/libs/agent/chat/memstore"
	"github.com/hollis-labs/libs/agent/chat/sqlitestore"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// noGrants accepts a standing grant and forgets it: enough for a client to
// exercise scope "always".
type noGrants struct{}

func (noGrants) Grant(_ context.Context, g chat.Grant) error {
	log.Printf("standing grant for approval %s in %s", g.ApprovalID, g.ConversationID)
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address; keep it on localhost, this server authorizes everyone")
	db := flag.String("db", "", "SQLite file for the event log and control records (empty: in memory)")
	whole := flag.Bool("whole", false, "play scripts as whole messages, with no part.delta")
	delay := flag.Duration("delay", 150*time.Millisecond, "pause between events, so a stream can be watched")
	flag.Parse()
	if err := run(*addr, *db, *whole, *delay); err != nil {
		log.Fatal(err)
	}
}

func run(addr, db string, whole bool, delay time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var (
		events  streamhub.Log = streamhub.NewMemoryLog()
		control chat.Control  = memstore.NewControl()
	)
	if db != "" {
		st, err := sqlitestore.Open(ctx, db)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		events, control = st, st
	}

	opts := []chattest.ScriptedOption{chattest.WithDelay(delay)}
	if whole {
		opts = append(opts, chattest.Whole())
	}
	runner := chattest.NewScripted(opts...)

	svc, err := chat.NewService(chat.Config{
		Runner: runner, Log: events, Control: control, Grants: noGrants{},
		// Every agent name is served, as an embedded agent of this example.
		Agents: func(context.Context, string) (chat.Agent, error) {
			return chat.Agent{Owner: "local", Scope: "chatserver", Kind: "embedded"}, nil
		},
		// The host hook: a turn starting with "/ping" is answered by the host,
		// one starting with "/no" is refused.
		Intercept: func(_ context.Context, turn *chat.Turn) chat.Outcome {
			switch {
			case len(turn.Text) >= 5 && turn.Text[:5] == "/ping":
				return chat.Answer("pong")
			case len(turn.Text) >= 3 && turn.Text[:3] == "/no":
				return chat.Reject("refused", "the host refused this turn")
			}
			return chat.Continue()
		},
	})
	if err != nil {
		return err
	}
	// A real host checks the caller its server authenticated. This one is for
	// localhost and lets everyone do everything.
	handler, err := chat.NewHandler(svc, func(context.Context, string, chat.Permission) error { return nil })
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/chat/", http.StripPrefix("/chat", handler))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("chat wire on http://%s/chat (scripted runner; try the turns: hello, tool, approval, confirm, reasoning, error, abort, length, hold)", ln.Addr())

	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	select {
	case serveErr := <-failed:
		return serveErr
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// End the runs first: their streams then close, and the server can drain.
	err = svc.Close(shutdown)
	_ = srv.Close()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
