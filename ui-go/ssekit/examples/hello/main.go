package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"

	ssekit "github.com/hollis-labs/go-ssekit"
)

func main() {
	// Server: an event stream over a channel. Serve writes each event, sends a
	// keepalive comment when quiet, and stops after the terminal event.
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w, err := ssekit.NewWriter(rw)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		// Resume: what the client already has. What the id means is up to you.
		from, _ := ssekit.ResumeCursor(r, ssekit.WithQueryKeys("from"))
		start, _ := strconv.Atoi(from)

		events := make(chan ssekit.Event, 8)
		for i := start + 1; i <= 3; i++ {
			events <- ssekit.Event{ID: strconv.Itoa(i), Name: "tick", Data: []byte("tick " + strconv.Itoa(i))}
		}
		events <- ssekit.Event{Name: "done", Data: []byte("bye")}

		_ = ssekit.Serve(r.Context(), w, ssekit.ChanSource(events),
			ssekit.WithTerminal(func(e ssekit.Event) bool { return e.Name == "done" }))
	}))
	defer srv.Close()

	// Client: Stream reconnects with the last event id if the connection drops.
	client := ssekit.NewClient(srv.Client())
	newReq := func(lastEventID string) (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL+"?from="+lastEventID, nil)
	}
	for ev, err := range client.Stream(context.Background(), newReq, ssekit.WithIsTerminal(func(e ssekit.Event) bool { return e.Name == "done" })) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("id=%q event=%s data=%s\n", ev.ID, ev.Name, ev.Data)
	}
}
