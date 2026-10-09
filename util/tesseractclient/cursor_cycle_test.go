package tesseract_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tesseract "github.com/hollis-labs/libs/util/tesseractclient"
	"github.com/hollis-labs/libs/util/tesseractclient/tesseracttest"
)

// A broken server or proxy can hand back a cursor the client has already used
// without it being the SAME cursor twice in a row (A, then B, then A). Comparing
// only with the previous cursor missed that: ListNamespaces then grew its result
// without bound, and RecallAll looped forever over empty pages.

// cycleServer answers routes with a fixed cursor cycle: the first request gets
// "A", then "B", then "A" again, forever.
func cycleServer(t *testing.T, withItems bool, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	cycle := []string{"A", "B"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		next := cycle[(n-1)%2]
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/memory/recall":
			results := []map[string]any{}
			if withItems {
				results = append(results, map[string]any{"revision": map[string]any{"revision_id": fmt.Sprintf("rev-%d", n)}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results":  results,
				"manifest": map[string]any{"next_cursor": next},
			})
		case "/v1/namespaces/list":
			items := []map[string]any{}
			if withItems {
				items = append(items, map[string]any{"namespace": fmt.Sprintf("ns-%d", n)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "truncated": true, "next_cursor": next})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRecallAllRefusesACursorCycle(t *testing.T) {
	for _, withItems := range []bool{true, false} {
		t.Run(fmt.Sprintf("items=%v", withItems), func(t *testing.T) {
			var hits atomic.Int64
			srv := cycleServer(t, withItems, &hits)
			_, _, err := tesseract.New(srv.URL, "").RecallAll(ctx(t), tesseract.RecallRequest{Namespaces: []string{root}}, 1_000_000)
			if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
				t.Fatalf("err = %v, want a repeated-cursor error", err)
			}
			if got := hits.Load(); got > 6 {
				t.Errorf("client made %d requests before giving up on an A, B, A cycle", got)
			}
		})
	}
}

func TestListNamespacesRefusesACursorCycle(t *testing.T) {
	for _, withItems := range []bool{true, false} {
		t.Run(fmt.Sprintf("items=%v", withItems), func(t *testing.T) {
			var hits atomic.Int64
			srv := cycleServer(t, withItems, &hits)
			got, err := tesseract.New(srv.URL, "").ListNamespaces(ctx(t), "")
			if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
				t.Fatalf("err = %v (result %v), want a repeated-cursor error", err, got)
			}
			if h := hits.Load(); h > 6 {
				t.Errorf("client made %d requests before giving up on an A, B, A cycle", h)
			}
		})
	}
}

// Every cursor is new, so no cycle exists: the server just never says it is
// done. ListNamespaces must still stop.
func TestListNamespacesStopsAServerThatNeverEnds(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":       []map[string]any{{"namespace": fmt.Sprintf("ns-%d", n)}},
			"truncated":   true,
			"next_cursor": fmt.Sprintf("cursor-%d", n),
		})
	}))
	t.Cleanup(srv.Close)
	_, err := tesseract.New(srv.URL, "").ListNamespaces(ctx(t), "")
	if err == nil || !strings.Contains(err.Error(), "namespace listing exceeded") {
		t.Fatalf("err = %v, want the page cap to stop the listing", err)
	}
	if got := hits.Load(); got > 1001 {
		t.Errorf("made %d requests; the page cap is 1000", got)
	}
}

// maxRecords <= 0 means the first page only: one page is always fetched, and
// complete is false if the server has more.
func TestRecallAllWithNoRecordBudgetReturnsTheFirstPage(t *testing.T) {
	for _, maxRecords := range []int{0, -1} {
		t.Run(fmt.Sprint(maxRecords), func(t *testing.T) {
			fake := tesseracttest.New(t, questions(5)...)
			got, complete, err := tesseract.New(fake.URL(), "").RecallAll(ctx(t),
				tesseract.RecallRequest{Namespaces: []string{root + "/*"}, Limit: 2}, maxRecords)
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				t.Error("complete = true with more pages on the server")
			}
			if len(got) != 2 {
				t.Errorf("got %d revisions, want the first page (2)", len(got))
			}
		})
	}
}
