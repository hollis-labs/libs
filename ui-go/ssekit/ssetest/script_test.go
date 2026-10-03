package ssetest_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ssekit "github.com/hollis-labs/go-ssekit"
	"github.com/hollis-labs/go-ssekit/ssetest"
)

func get(t *testing.T, srv *httptest.Server, header string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if header != "" {
		req.Header.Set("Last-Event-ID", header)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestScript_StepsSpanConnectionsAndRecordRequests(t *testing.T) {
	s := ssetest.Script(
		ssetest.Emit(2), ssetest.Close(),
		ssetest.Status(503),
		ssetest.Overlap(1), ssetest.Gap(4, 5), ssetest.Emit(2), ssetest.Comment("c"), ssetest.Retry(1500e6), ssetest.Close(),
	)
	srv := httptest.NewServer(s)
	defer srv.Close()

	resp, body := get(t, srv, "")
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
	}
	if want := "id: 1\nevent: tick\ndata: payload-1\n\nid: 2\nevent: tick\ndata: payload-2\n\n"; body != want {
		t.Fatalf("conn 1 = %q", body)
	}
	if resp, _ := get(t, srv, "2"); resp.StatusCode != 503 {
		t.Fatalf("conn 2 status = %d", resp.StatusCode)
	}
	_, body = get(t, srv, "2")
	// Overlap(1) rewinds to id 2; Gap(4,5) then skips 4 and 5 after 3.
	if !strings.HasPrefix(body, "id: 2\n") || !strings.Contains(body, "id: 3\n") || strings.Contains(body, "id: 4\n") ||
		!strings.Contains(body, ": c\n\n") || !strings.HasSuffix(body, "retry: 1500\n\n") {
		t.Fatalf("conn 3 = %q", body)
	}
	if resp, _ := get(t, srv, ""); resp.StatusCode != http.StatusGone {
		t.Fatalf("exhausted status = %d", resp.StatusCode)
	}
	reqs := s.Requests()
	if len(reqs) != 4 || reqs[1].LastEventID != "2" || reqs[0].LastEventID != "" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestScript_UsesTheSameWireFormatAsTheWriter(t *testing.T) {
	srv := httptest.NewServer(ssetest.Script(ssetest.Send(ssekit.Event{ID: "9", Name: "n", Data: []byte("a\nb")}), ssetest.Close()))
	defer srv.Close()
	_, body := get(t, srv, "")
	if want := "id: 9\nevent: n\ndata: a\ndata: b\n\n"; body != want {
		t.Fatalf("body = %q", body)
	}
}
