package tesseracttest_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/util/tesseractclient/tesseracttest"
)

func do(t *testing.T, method, url, body string, hdr ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFakeCountsAndRecordsRequests(t *testing.T) {
	f := tesseracttest.New(t, tesseracttest.NewRevision("a/b", "k"))
	do(t, "GET", f.URL()+"/v1/health/readiness", "")
	do(t, "GET", f.URL()+"/v1/knowledge/current?namespace=a/b&key=k", "", "Authorization", "Bearer z")
	if f.Calls(tesseracttest.RouteReadiness) != 1 || f.Calls(tesseracttest.RouteCurrent) != 1 || f.Calls(tesseracttest.RouteRecall) != 0 {
		t.Errorf("counts wrong: %d %d", f.Calls(tesseracttest.RouteReadiness), f.Calls(tesseracttest.RouteCurrent))
	}
	reqs := f.Requests(tesseracttest.RouteCurrent)
	if len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "Bearer z" || !strings.Contains(reqs[0].RawQuery, "key=k") {
		t.Errorf("requests = %+v", reqs)
	}
	if len(f.Requests()) != 2 {
		t.Errorf("all requests = %d, want 2", len(f.Requests()))
	}
}

func TestFakeFailAndClear(t *testing.T) {
	f := tesseracttest.New(t)
	f.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{})
	if code, _ := do(t, "GET", f.URL()+"/v1/health/readiness", ""); code != 500 {
		t.Errorf("default failure status = %d, want 500", code)
	}
	f.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{Status: 503, Code: "down", Message: "m"})
	if code, body := do(t, "GET", f.URL()+"/v1/health/readiness", ""); code != 503 || !strings.Contains(body, `"code":"down"`) {
		t.Errorf("got %d %s", code, body)
	}
	f.ClearFailures()
	if code, _ := do(t, "GET", f.URL()+"/v1/health/readiness", ""); code != 200 {
		t.Errorf("after ClearFailures status = %d", code)
	}
}

func TestFakeRequireTokenLeavesReadinessPublic(t *testing.T) {
	f := tesseracttest.New(t)
	f.RequireToken("s")
	if code, _ := do(t, "GET", f.URL()+"/v1/health/readiness", ""); code != 200 {
		t.Errorf("readiness = %d, want public", code)
	}
	if code, _ := do(t, "GET", f.URL()+"/v1/namespaces/list", ""); code != 401 {
		t.Errorf("no token = %d, want 401", code)
	}
	if code, _ := do(t, "GET", f.URL()+"/v1/namespaces/list", "", "Authorization", "Bearer s"); code != 200 {
		t.Errorf("token = %d, want 200", code)
	}
	f.RequireToken("")
	if code, _ := do(t, "GET", f.URL()+"/v1/namespaces/list", ""); code != 200 {
		t.Errorf("requirement off = %d, want 200", code)
	}
}

func TestFakeRecallExpandsWildcardsExcludingTheNamespaceItself(t *testing.T) {
	f := tesseracttest.New(t,
		tesseracttest.NewRevision("a/b", "top"),
		tesseracttest.NewRevision("a/b/c", "deep"),
	)
	_, body := do(t, "POST", f.URL()+"/v1/memory/recall", `{"namespaces":["a/b/*"]}`)
	if strings.Contains(body, `"top"`) || !strings.Contains(body, `"deep"`) {
		t.Errorf("body = %s", body)
	}
}

func TestFakeRecallRejectsUnknownFields(t *testing.T) {
	f := tesseracttest.New(t)
	code, body := do(t, "POST", f.URL()+"/v1/memory/recall", `{"namespaces":["a"],"origins":["x"]}`)
	if code != 400 || !strings.Contains(body, "validation_error") || !strings.Contains(body, "origins") {
		t.Errorf("got %d %s", code, body)
	}
}

func TestFakeFiltersKeysAreCaseInsensitiveButNotAcrossUnderscores(t *testing.T) {
	f := tesseracttest.New(t)
	if code, _ := do(t, "POST", f.URL()+"/v1/memory/recall", `{"namespaces":["a"],"filters":{"confidencemin":0.5}}`); code != 200 {
		t.Errorf("case-insensitive key = %d, want 200 like encoding/json on the server", code)
	}
	if code, _ := do(t, "POST", f.URL()+"/v1/memory/recall", `{"namespaces":["a"],"filters":{"confidence_min":0.5}}`); code != 400 {
		t.Errorf("snake_case key = %d, want 400", code)
	}
}

func TestFakePageSizeCapsPages(t *testing.T) {
	f := tesseracttest.New(t,
		tesseracttest.NewRevision("a", "1"), tesseracttest.NewRevision("a", "2"), tesseracttest.NewRevision("a", "3"))
	f.PageSize(2)
	_, body := do(t, "POST", f.URL()+"/v1/memory/recall", `{"namespaces":["a"],"limit":100}`)
	if !strings.Contains(body, `"results_returned":2`) || !strings.Contains(body, `"truncated":true`) || strings.Contains(body, `"next_cursor":null`) {
		t.Errorf("body = %s", body)
	}
}

func TestFakeDelayHonoursClientCancellation(t *testing.T) {
	f := tesseracttest.New(t)
	f.Delay(3 * time.Second)
	c := &http.Client{Timeout: 100 * time.Millisecond}
	start := time.Now()
	if _, err := c.Get(f.URL() + "/v1/health/readiness"); err == nil {
		t.Fatal("want a client timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("client did not time out promptly")
	}
}

func TestFakeSetAndRevisions(t *testing.T) {
	f := tesseracttest.New(t, tesseracttest.NewRevision("a", "1"))
	f.Set(tesseracttest.NewRevision("a", "2", tesseracttest.Status("deprecated")), tesseracttest.NewRevision("a", "3"))
	revs := f.Revisions()
	if len(revs) != 2 || revs[0].MemoryKey != "2" {
		t.Errorf("revisions = %+v", revs)
	}
	_, body := do(t, "POST", f.URL()+"/v1/memory/recall", `{"namespaces":["a"]}`)
	if strings.Contains(body, `"2"`) && strings.Contains(body, `"memory_key":"2"`) {
		t.Errorf("deprecated revision recalled: %s", body)
	}
}

func TestBuilders(t *testing.T) {
	r := tesseracttest.NewRevision("n/s", "k",
		tesseracttest.RevisionID("R"), tesseracttest.Summary("S"), tesseracttest.Body("B"),
		tesseracttest.Tags("t"), tesseracttest.Created("2026-01-01T00:00:00Z"),
		tesseracttest.Data(map[string]any{"a": 1}), tesseracttest.Data(map[string]any{"b": 2}),
		tesseracttest.State(map[string]any{"s": true}))
	if r.RevisionID != "R" || r.Payload.Summary != "S" || r.Payload.Body != "B" || r.Tags[0] != "t" || r.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("rev = %+v", r)
	}
	if string(r.Payload.Data) != `{"a":1,"b":2}` || string(r.ConsumerState) != `{"s":true}` {
		t.Errorf("data=%s state=%s", r.Payload.Data, r.ConsumerState)
	}
}
