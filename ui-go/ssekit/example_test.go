package ssekit_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	ssekit "github.com/hollis-labs/go-ssekit"
	"github.com/hollis-labs/go-ssekit/ssetest"
)

func ExampleRead() {
	stream := "retry: 2000\n\n" +
		": a comment\n\n" +
		"id: 7\nevent: greeting\ndata: hello\ndata: world\n\n" +
		"data: cut off" // incomplete at EOF: discarded
	for ev, err := range ssekit.Read(strings.NewReader(stream)) {
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Printf("id=%s name=%s retry=%v data=%q\n", ev.ID, ev.Name, ev.Retry, ev.Data)
	}
	// Output: id=7 name=greeting retry=2s data="hello\nworld"
}

func ExampleWriter() {
	rec := httptest.NewRecorder() // any http.ResponseWriter that can flush
	w, err := ssekit.NewWriter(rec)
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = w.Send(ssekit.Event{ID: "1", Name: "update", Data: []byte(`{"n":1}`)})
	_ = w.Send(ssekit.Event{Name: "control", Data: []byte("no id: the client's Last-Event-ID is untouched")})
	_ = w.Comment("keepalive")
	fmt.Print(rec.Body.String())
	// Output:
	// id: 1
	// event: update
	// data: {"n":1}
	//
	// event: control
	// data: no id: the client's Last-Event-ID is untouched
	//
	// : keepalive
}

func ExampleServe() {
	rec := httptest.NewRecorder()
	w, _ := ssekit.NewWriter(rec)

	events := make(chan ssekit.Event, 3)
	events <- ssekit.Event{ID: "1", Name: "delta", Data: []byte("a")}
	events <- ssekit.Event{ID: "2", Name: "done", Data: []byte("b")}
	events <- ssekit.Event{ID: "3", Name: "never sent", Data: []byte("c")}

	err := ssekit.Serve(context.Background(), w, ssekit.ChanSource(events),
		ssekit.WithHeartbeat(15*time.Second),
		ssekit.WithTerminal(func(e ssekit.Event) bool { return e.Name == "done" }))
	fmt.Println("serve:", err)
	fmt.Print(rec.Body.String())
	// Output:
	// serve: <nil>
	// id: 1
	// event: delta
	// data: a
	//
	// id: 2
	// event: done
	// data: b
}

func ExampleResumeCursor() {
	r := httptest.NewRequest(http.MethodGet, "/events?from=10", nil)
	r.Header.Set("Last-Event-ID", "14") // a browser sends this on auto-reconnect

	cursor, ok := ssekit.ResumeCursor(r, ssekit.WithQueryKeys("from", "after"))
	fmt.Println(cursor, ok) // the header is at least as new as the URL's initial cursor

	cursor, _ = ssekit.ResumeCursor(r, ssekit.WithQueryKeys("from"), ssekit.WithPrecedence(ssekit.QueryFirst))
	fmt.Println(cursor)
	// Output:
	// 14 true
	// 10
}

func ExampleClient_Stream() {
	// A scripted server that drops the connection after two events.
	srv := httptest.NewServer(ssetest.Script(ssetest.Drop(2), ssetest.Emit(2)))
	defer srv.Close()

	client := ssekit.NewClient(srv.Client())
	newReq := func(lastEventID string) (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	}
	for ev, err := range client.Stream(context.Background(), newReq,
		ssekit.WithBackoff([]time.Duration{time.Millisecond}, 0),
		ssekit.WithIsTerminal(func(e ssekit.Event) bool { return e.ID == "4" })) {
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Println(ev.ID, string(ev.Data))
	}
	// Output:
	// 1 payload-1
	// 2 payload-2
	// 3 payload-3
	// 4 payload-4
}

func ExampleMerge() {
	a := make(chan ssekit.Event, 1)
	b := make(chan ssekit.Event, 1)
	a <- ssekit.Event{Name: "presence", Data: []byte("online")}
	close(a)
	close(b)

	rec := httptest.NewRecorder()
	w, _ := ssekit.NewWriter(rec)
	_ = ssekit.Serve(context.Background(), w, ssekit.Merge(ssekit.ChanSource(a), ssekit.ChanSource(b)))
	fmt.Print(rec.Body.String())
	// Output:
	// event: presence
	// data: online
}

func ExamplePollSource() {
	polls := 0
	src := ssekit.PollSource(func(_ context.Context, cursor string) ([]ssekit.Event, string, error) {
		polls++
		if polls > 1 {
			return nil, cursor, context.Canceled // stop the example
		}
		return []ssekit.Event{{Name: "revision", Data: []byte("r1")}}, "r1", nil
	}, time.Second)

	ev, _ := src.Next(context.Background())
	fmt.Println(ev.Name, string(ev.Data))
	_, err := src.Next(context.Background())
	fmt.Println(err)
	// Output:
	// revision r1
	// context canceled
}
