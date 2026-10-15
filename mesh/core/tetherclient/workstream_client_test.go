package tether

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestCreateWorkstream(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/workstreams")
		if body["name"] != "spike" || body["workflow_id"] != "wf1" {
			t.Errorf("body = %v", body)
		}
		sendJSON(t, w, http.StatusCreated, Workstream{ID: "ws1", Name: "spike", WorkflowID: "wf1", Status: "active"})
	})
	got, err := c.CreateWorkstream(context.Background(), "spike", "wf1")
	if err != nil || got.ID != "ws1" || got.Status != "active" {
		t.Fatalf("CreateWorkstream = %+v, %v", got, err)
	}
}

func TestGetWorkstream(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/workstreams/ws1")
		sendJSON(t, w, http.StatusOK, Workstream{ID: "ws1"})
	})
	if got, err := c.GetWorkstream(context.Background(), "ws1"); err != nil || got.ID != "ws1" {
		t.Fatalf("GetWorkstream = %+v, %v", got, err)
	}
}

func TestListWorkstreams(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/workstreams")
		q := r.URL.Query()
		if q.Get("status") != "active" || q.Get("workflow_id") != "wf1" {
			t.Errorf("query = %v", q)
		}
		sendJSON(t, w, http.StatusOK, workstreamListResponse{Workstreams: []Workstream{{ID: "a"}, {ID: "b"}}})
	})
	got, err := c.ListWorkstreams(context.Background(), "active", "wf1")
	if err != nil || len(got) != 2 {
		t.Fatalf("ListWorkstreams = %+v, %v", got, err)
	}
}

func TestListWorkstreamsNoFiltersSendsNoQuery(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		sendJSON(t, w, http.StatusOK, workstreamListResponse{})
	})
	if _, err := c.ListWorkstreams(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestAssignSessionWorkstream(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/sessions/s1/workstream")
		if body["workstream_id"] != "ws1" {
			t.Errorf("body = %v", body)
		}
		if _, set := body["ensure"]; set {
			t.Errorf("assign must not ask to ensure: %v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.AssignSessionWorkstream(context.Background(), "s1", "ws1"); err != nil {
		t.Fatal(err)
	}
}

func TestAssignSessionWorkstreamEmptyClears(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if len(body) != 0 {
			t.Errorf("clearing sends an empty object, got %v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.AssignSessionWorkstream(context.Background(), "s1", ""); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSessionWorkstream(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/sessions/s1/workstream")
		if body["ensure"] != true || body["name"] != "n" || body["workflow_id"] != "wf" {
			t.Errorf("body = %v", body)
		}
		sendJSON(t, w, http.StatusOK, Workstream{ID: "ws9"})
	})
	got, err := c.EnsureSessionWorkstream(context.Background(), "s1", "n", "wf")
	if err != nil || got.ID != "ws9" {
		t.Fatalf("EnsureSessionWorkstream = %+v, %v", got, err)
	}
}

func TestSessionWorkstreamNamespace(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/sessions/s1/workstream-namespace")
		q := r.URL.Query()
		if q.Get("project") != "p" || q.Get("owner") != "o" || q.Get("tail") != "notes" {
			t.Errorf("query = %v", q)
		}
		sendJSON(t, w, http.StatusOK, WorkstreamNamespaceResponse{Namespace: "ns/a", WorkstreamID: "ws1"})
	})
	got, err := c.SessionWorkstreamNamespace(context.Background(), "s1", SessionWorkstreamNamespaceOptions{Project: "p", Owner: "o", Tail: "notes"})
	if err != nil || got.Namespace != "ns/a" || got.WorkstreamID != "ws1" {
		t.Fatalf("SessionWorkstreamNamespace = %+v, %v", got, err)
	}
}

func TestSessionWorkstreamNamespaceNoOptions(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		sendJSON(t, w, http.StatusOK, WorkstreamNamespaceResponse{})
	})
	if _, err := c.SessionWorkstreamNamespace(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
}

func digestFixture() DigestResponse {
	return DigestResponse{
		Grain:      "workstream",
		Workstream: &Workstream{ID: "ws1", Status: "active"},
		Span:       DigestSpan{SessionCount: 2, SpansLineage: true, Sessions: []DigestSession{{ID: "s1", RefAttribution: "unknown", RefCount: 1}, {ID: "s2", ParentSessionID: "s1", RefAttribution: "proxy"}}},
		LeftBehind: []DigestKindGroup{{Kind: "task", Created: []DigestRef{{SessionID: "s1", RefID: "T1", Relation: "created", Source: "agent", At: "2026-09-30T00:00:00Z"}}}},
		Touched:    []DigestKindGroup{{Kind: "doc", Read: []DigestRef{{RefID: "D1"}}}},
		Totals:     DigestTotals{Refs: 2, ByRelation: map[string]int{"created": 1, "read": 1}, BySource: map[string]int{"agent": 2}},
		Coverage:   DigestCoverage{Limit: 100, Truncated: true, Attribution: map[string]int{"unknown": 1}, ProxyAttributable: 1, Note: "n"},
	}
}

func TestDigests(t *testing.T) {
	q := DigestQuery{Kind: "task", Relation: "created", Source: "agent", Since: "2026-09-30T00:00:00Z", Limit: 50}
	check := func(r *http.Request) {
		got := r.URL.Query()
		if got.Get("kind") != "task" || got.Get("relation") != "created" || got.Get("source") != "agent" ||
			got.Get("since") != "2026-09-30T00:00:00Z" || got.Get("limit") != "50" {
			t.Errorf("query = %v", got)
		}
	}
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		check(r)
		switch r.URL.Path {
		case "/sessions/s1/digest", "/workstreams/ws1/digest":
			sendJSON(t, w, http.StatusOK, digestFixture())
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	for name, call := range map[string]func() (DigestResponse, error){
		"session":    func() (DigestResponse, error) { return c.SessionDigest(context.Background(), "s1", q) },
		"workstream": func() (DigestResponse, error) { return c.WorkstreamDigest(context.Background(), "ws1", q) },
	} {
		got, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := digestFixture()
		if got.Grain != want.Grain || got.Workstream == nil || got.Workstream.ID != "ws1" ||
			got.Span.SessionCount != 2 || !got.Span.SpansLineage || len(got.Span.Sessions) != 2 || got.Span.Sessions[1].ParentSessionID != "s1" ||
			len(got.LeftBehind) != 1 || got.LeftBehind[0].Created[0].RefID != "T1" || got.Touched[0].Read[0].RefID != "D1" ||
			got.Totals.Refs != 2 || got.Totals.ByRelation["read"] != 1 ||
			!got.Coverage.Truncated || got.Coverage.ProxyAttributable != 1 || got.Coverage.Note != "n" {
			t.Errorf("%s: digest did not round-trip: %+v", name, got)
		}
	}
}

func TestDigestZeroQuerySendsNoQuery(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		sendJSON(t, w, http.StatusOK, DigestResponse{Grain: "session", SessionID: "s1"})
	})
	got, err := c.SessionDigest(context.Background(), "s1", DigestQuery{})
	if err != nil || got.SessionID != "s1" || got.Workstream != nil {
		t.Fatalf("SessionDigest = %+v, %v", got, err)
	}
}

func TestWorkstreamsForRef(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/workstreams")
		if r.URL.Query().Get("ref") != "task:CW-1" {
			t.Errorf("ref = %q", r.URL.Query().Get("ref"))
		}
		sendJSON(t, w, http.StatusOK, workstreamListResponse{Workstreams: []Workstream{{ID: "a"}, {ID: "b"}}})
	})
	got, err := c.WorkstreamsForRef(context.Background(), "task:CW-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("WorkstreamsForRef = %+v, %v; want every match", got, err)
	}
}

func TestWorkstreamErrorsAreAPIErrors(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		sendErr(t, w, http.StatusNotFound, "not_found", "workstreams are not enabled on this server")
	})
	ctx := context.Background()
	calls := map[string]func() error{
		"get":    func() error { _, err := c.GetWorkstream(ctx, "x"); return err },
		"list":   func() error { _, err := c.ListWorkstreams(ctx, "", ""); return err },
		"create": func() error { _, err := c.CreateWorkstream(ctx, "n", ""); return err },
		"assign": func() error { return c.AssignSessionWorkstream(ctx, "s", "w") },
		"digest": func() error { _, err := c.WorkstreamDigest(ctx, "w", DigestQuery{}); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, &APIError{StatusCode: http.StatusNotFound, Code: "not_found"}) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestWorkstreamDaemonUnreachable(t *testing.T) {
	c := MustNew("tcp:127.0.0.1:1")
	if _, err := c.GetWorkstream(context.Background(), "x"); !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("err = %v, want ErrDaemonUnreachable", err)
	}
}
