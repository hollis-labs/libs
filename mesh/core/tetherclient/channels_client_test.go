package tether_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tether "github.com/hollis-labs/go-tether-client"
)

func TestChannelHistoryAndList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("as") != "msg://agent/local/consumer" {
			t.Errorf("caller = %q", r.URL.Query().Get("as"))
		}
		switch r.URL.Path {
		case "/channels":
			fmt.Fprint(w, `{"channels":[{"name":"ops","address":"msg://service/local/channel/ops"}]}`)
		case "/channels/ops/messages":
			if r.URL.Query().Get("since") != "7" || r.URL.Query().Get("limit") != "2" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"name":"ops","address":"msg://service/local/channel/ops","messages":[{"seq":9,"id":"m1","kind":"notice","from":"msg://agent/local/sender","to":"msg://service/local/channel/ops","payload":"hello"}],"next_since":9}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := tether.MustNew(srv.URL, tether.WithToken(""), tether.WithSelfURN("msg://agent/local/consumer"))
	channels, err := c.ListChannels(context.Background())
	if err != nil || len(channels) != 1 || channels[0].Name != "ops" {
		t.Fatalf("list = %v, %v", channels, err)
	}
	page, err := c.ChannelMessages(context.Background(), "ops", tether.ChannelMessagesOptions{Since: 7, Limit: 2})
	if err != nil || page.NextSince != 9 || len(page.Messages) != 1 || string(page.Messages[0].Payload) != `"hello"` || page.Messages[0].Seq != 9 {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestChannelLatestHistoryAndPurgeReceipt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("last") != "2" || r.URL.Query().Has("since") || r.URL.Query().Has("limit") {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"name":"ops","address":"msg://service/local/channel/ops","messages":[{"seq":8,"purged":true,"purged_at":"2026-10-02T15:00:00Z"}],"next_since":8}`)
	}))
	defer srv.Close()
	c := tether.MustNew(srv.URL, tether.WithToken(""))
	page, err := c.ChannelMessages(context.Background(), "ops", tether.ChannelMessagesOptions{Last: 2})
	if err != nil || len(page.Messages) != 1 || !page.Messages[0].Purged || page.Messages[0].PurgedAt == nil || page.NextSince != 8 {
		t.Fatalf("page=%+v %v", page, err)
	}
	if _, err := c.ChannelMessages(context.Background(), "ops", tether.ChannelMessagesOptions{Last: 2, Since: 8}); err == nil {
		t.Fatal("last/since combination accepted")
	}
}

func TestSubscribeChannelReplayAndCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channels/ops/subscribe" || r.URL.Query().Get("since") != "0" {
			t.Errorf("URL = %s", r.URL)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond) // deliberately exceed HTTP timeout
		payload, _ := json.Marshal(map[string]any{"seq": 42, "payload": strings.Repeat("x", 80*1024)})
		fmt.Fprintf(w, ": ping\n\nevent: unrelated\ndata: ignored\n\nid: 42\nevent: message\ndata: %s\n\n", payload)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := tether.MustNew(srv.URL, tether.WithToken(""), tether.WithHTTPClient(&http.Client{Timeout: 10 * time.Millisecond}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	zero := int64(0)
	events, errs, err := c.SubscribeChannel(ctx, "ops", &zero)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-events:
		if msg.Seq != 42 || len(msg.Payload) != 80*1024+2 {
			t.Fatalf("message seq=%d size=%d", msg.Seq, len(msg.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not deliver")
	}
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("events still open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not close stream")
	}
	if err := <-errs; err != nil {
		t.Fatalf("cancellation error: %v", err)
	}
}

func TestSubscribeChannelLiveAndErrors(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, found := r.URL.Query()["since"]; found {
					t.Error("live subscription sent since")
				}
				if malformed {
					fmt.Fprint(w, "event: message\ndata: {broken\n\n")
					return
				}
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"error":"forbidden"}`)
			}))
			defer srv.Close()
			c := tether.MustNew(srv.URL, tether.WithToken(""))
			events, errs, err := c.SubscribeChannel(context.Background(), "ops", nil)
			if !malformed {
				if err == nil {
					t.Fatal("HTTP failure lost")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := <-events; ok {
				t.Fatal("malformed event delivered")
			}
			if err := <-errs; err == nil || !strings.Contains(err.Error(), "decode channel message") {
				t.Fatalf("stream error = %v", err)
			}
		})
	}
}
