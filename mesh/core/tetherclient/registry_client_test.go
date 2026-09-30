package tether

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testAgentURN   = "msg://agent/agent-mux/agt_01"
	testProjectURN = "msg://agent/agent-mux/prj_01"
)

// regServer starts a daemon stand-in and returns a client on it. The handler
// gets the decoded request body (nil when there is none).
func regServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body map[string]any)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil && r.ContentLength != 0 {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		h(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return MustNew(srv.URL)
}

func expect(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method || r.URL.Path != path {
		t.Fatalf("request = %s %s, want %s %s", r.Method, r.URL.Path, method, path)
	}
}

func sendJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func sendErr(t *testing.T, w http.ResponseWriter, status int, code, msg string) {
	t.Helper()
	sendJSON(t, w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: msg}})
}

func TestRegistryRegister(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/registry/agents")
		if body["display_name"] != "Ada" || body["role"] != "reviewer" {
			t.Errorf("body = %v", body)
		}
		if _, set := body["urn"]; !set {
			t.Errorf("urn is a required wire key (empty), missing from %v", body)
		}
		sendJSON(t, w, http.StatusCreated, RegistryProfile{URN: testAgentURN, Kind: RegistryKindAgent, DisplayName: "Ada", Status: RegistryStatusActive})
	})
	got, err := c.Registry().Register(context.Background(), RegistryKindAgent, RegistryProfile{DisplayName: "Ada", Role: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if got.URN != testAgentURN || got.Status != RegistryStatusActive {
		t.Errorf("profile = %+v", got)
	}
}

func TestRegistryRegisterRejectsUnsupportedKindWithoutARequest(t *testing.T) {
	c := regServer(t, func(http.ResponseWriter, *http.Request, map[string]any) {
		t.Error("an unsupported kind must not reach the daemon")
	})
	if _, err := c.Registry().Register(context.Background(), RegistryKind("robot"), RegistryProfile{}); err == nil {
		t.Fatal("want an error for an unsupported kind")
	}
}

func TestRegistryLookup(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/registry/agents/"+testAgentURN)
		if r.URL.RawQuery != "" {
			t.Errorf("Lookup sends no query, got %q", r.URL.RawQuery)
		}
		sendJSON(t, w, http.StatusOK, RegistryProfile{URN: testAgentURN, Kind: RegistryKindAgent})
	})
	got, err := c.Registry().Lookup(context.Background(), testAgentURN)
	if err != nil || got.URN != testAgentURN {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}
}

func TestRegistryLookupKindFromURNPrefix(t *testing.T) {
	var path string
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		path = r.URL.Path
		sendJSON(t, w, http.StatusOK, RegistryProfile{})
	})
	if _, err := c.Registry().Lookup(context.Background(), testProjectURN); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/registry/projects/") {
		t.Errorf("a prj_ URN must go to /registry/projects, got %s", path)
	}
	// grp_ is not a /registry kind: groups are served from /groups.
	if _, err := c.Registry().Lookup(context.Background(), "msg://agent/agent-mux/grp_01"); err == nil {
		t.Error("a grp_ URN must be refused by the registry client")
	}
	if _, err := c.Registry().Lookup(context.Background(), "nonsense"); err == nil {
		t.Error("a URN with no known prefix must be refused")
	}
}

func TestRegistryLookupWithInclude(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/registry/agents/"+testAgentURN)
		if got := r.URL.Query().Get("include"); got != "callback,external_ids" {
			t.Errorf("include = %q", got)
		}
		sendJSON(t, w, http.StatusOK, RegistryProfile{URN: testAgentURN, Callback: &RegistryCallback{Scheme: "file", Target: "file:///x"}})
	})
	got, err := c.Registry().LookupWithInclude(context.Background(), testAgentURN, "callback", "external_ids")
	if err != nil || got.Callback == nil || got.Callback.Scheme != "file" {
		t.Fatalf("LookupWithInclude = %+v, %v", got, err)
	}
}

func TestRegistryLookupBy(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/registry/agents")
		q := r.URL.Query()
		if q.Get("external_id") != "ext-1" || q.Get("substrate") != "tether" || q.Get("full") != "" {
			t.Errorf("query = %v", q)
		}
		// keyed on the SINGULAR kind
		sendJSON(t, w, http.StatusOK, map[string]any{"agent": RegistryProfile{URN: testAgentURN}})
	})
	got, err := c.Registry().LookupBy(context.Background(), RegistryKindAgent, "ext-1", "tether")
	if err != nil || got.URN != testAgentURN {
		t.Fatalf("LookupBy = %+v, %v", got, err)
	}
}

func TestRegistryLookupByAllAsksForFull(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		q := r.URL.Query()
		if q.Get("full") != "true" || q.Has("include") || q.Has("substrate") {
			t.Errorf("query = %v", q)
		}
		sendJSON(t, w, http.StatusOK, map[string]any{"project": RegistryProfile{URN: testProjectURN}})
	})
	if _, err := c.Registry().LookupByWithInclude(context.Background(), RegistryKindProject, "p", "", "all"); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryLookupByWrongEnvelopeKeyIsAnError(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		// the PLURAL key, as Search uses, is not what LookupBy decodes
		sendJSON(t, w, http.StatusOK, map[string]any{"agents": RegistryProfile{URN: testAgentURN}})
	})
	if _, err := c.Registry().LookupBy(context.Background(), RegistryKindAgent, "x", ""); err == nil {
		t.Fatal("a response without the singular kind key must be an error, not a zero profile")
	}
}

func TestRegistrySearch(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/registry/agents")
		q := r.URL.Query()
		if q.Get("role") != "reviewer" || q.Get("tag") != "go" || q.Get("skill_name") != "review" || q.Has("title") {
			t.Errorf("query = %v", q)
		}
		sendJSON(t, w, http.StatusOK, map[string]any{"agents": []RegistryProfile{{URN: testAgentURN}, {URN: "b"}}})
	})
	got, err := c.Registry().Search(context.Background(), RegistryKindAgent, RegistryFilter{Role: "reviewer", Tag: "go", SkillName: "review"})
	if err != nil || len(got) != 2 {
		t.Fatalf("Search = %+v, %v", got, err)
	}
}

func TestRegistrySearchEmptyIsNonNil(t *testing.T) {
	for name, body := range map[string]any{
		"null list":   map[string]any{"agents": nil},
		"empty list":  map[string]any{"agents": []RegistryProfile{}},
		"missing key": map[string]any{},
	} {
		t.Run(name, func(t *testing.T) {
			c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				sendJSON(t, w, http.StatusOK, body)
			})
			got, err := c.Registry().Search(context.Background(), RegistryKindAgent, RegistryFilter{})
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("Search = %#v, %v; want a non-nil empty slice", got, err)
			}
		})
	}
}

func TestRegistryUpdateSelf(t *testing.T) {
	title := "Lead"
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPatch, "/registry/agents/"+testAgentURN)
		if body["title"] != "Lead" || body["last_updated_by"] != testAgentURN {
			t.Errorf("body = %v", body)
		}
		if _, set := body["description"]; set {
			t.Errorf("an untouched field must not be sent: %v", body)
		}
		caps, _ := body["capabilities"].(map[string]any)
		if caps["mode"] != "append" {
			t.Errorf("capabilities = %v, want the explicit append wrapper", body["capabilities"])
		}
		sendJSON(t, w, http.StatusOK, RegistryProfile{URN: testAgentURN, Title: "Lead"})
	})
	got, err := c.Registry().UpdateSelf(context.Background(), testAgentURN, RegistryUpdatePatch{
		Title:         &title,
		LastUpdatedBy: testAgentURN,
		Capabilities:  &RegistryArrayPatch[string]{Mode: RegistryArrayModeAppend, Value: []string{"go"}},
	})
	if err != nil || got.Title != "Lead" {
		t.Fatalf("UpdateSelf = %+v, %v", got, err)
	}
}

func TestRegistryUpdateSelfClearsWithPointerToEmpty(t *testing.T) {
	empty := ""
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if v, set := body["title"]; !set || v != "" {
			t.Errorf("a pointer to \"\" is an explicit clear and must be sent: %v", body)
		}
		sendJSON(t, w, http.StatusOK, RegistryProfile{})
	})
	if _, err := c.Registry().UpdateSelf(context.Background(), testAgentURN, RegistryUpdatePatch{Title: &empty, LastUpdatedBy: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryArrayPatchDecodesBothShapes(t *testing.T) {
	var explicit, shorthand RegistryArrayPatch[string]
	if err := json.Unmarshal([]byte(`{"mode":"remove","value":["a"]}`), &explicit); err != nil || explicit.Mode != RegistryArrayModeRemove || len(explicit.Value) != 1 {
		t.Fatalf("explicit = %+v, %v", explicit, err)
	}
	if err := json.Unmarshal([]byte(`  ["a","b"]`), &shorthand); err != nil || shorthand.Mode != RegistryArrayModeReplace || len(shorthand.Value) != 2 {
		t.Fatalf("shorthand = %+v, %v", shorthand, err)
	}
	var bad RegistryArrayPatch[string]
	if err := json.Unmarshal([]byte(`{"mode":"upsert","value":[]}`), &bad); err == nil {
		t.Error("an unknown mode must be rejected")
	}
}

func TestRegistryDeregister(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodDelete, "/registry/agents/"+testAgentURN)
		sendJSON(t, w, http.StatusOK, RegistryProfile{URN: testAgentURN, Status: RegistryStatusDeprecated})
	})
	got, err := c.Registry().Deregister(context.Background(), testAgentURN)
	if err != nil || got.Status != RegistryStatusDeprecated {
		t.Fatalf("Deregister = %+v, %v", got, err)
	}
}

func TestRegistryMerge(t *testing.T) {
	dst := "msg://agent/agent-mux/agt_02"
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/registry/agents/"+testAgentURN+"/merge")
		if body["into"] != dst {
			t.Errorf("body = %v", body)
		}
		sendJSON(t, w, http.StatusOK, RegistryProfile{URN: dst})
	})
	got, err := c.Registry().Merge(context.Background(), testAgentURN, dst)
	if err != nil || got.URN != dst {
		t.Fatalf("Merge = %+v, %v", got, err)
	}
}

func TestRegistrySyncTwoShapes(t *testing.T) {
	t.Run("204 means no callback", func(t *testing.T) {
		c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			expect(t, r, http.MethodPost, "/registry/agents/"+testAgentURN+"/sync")
			w.WriteHeader(http.StatusNoContent)
		})
		got, synced, err := c.Registry().Sync(context.Background(), testAgentURN)
		if err != nil || synced || got.URN != "" {
			t.Fatalf("Sync = %+v, %v, %v; want zero profile, synced=false", got, synced, err)
		}
	})
	t.Run("200 carries the refreshed profile", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			sendJSON(t, w, http.StatusOK, RegistryProfile{URN: testAgentURN, CachedAt: &now})
		})
		got, synced, err := c.Registry().Sync(context.Background(), testAgentURN)
		if err != nil || !synced || got.URN != testAgentURN || got.CachedAt == nil || !got.CachedAt.Equal(now) {
			t.Fatalf("Sync = %+v, %v, %v", got, synced, err)
		}
	})
	t.Run("an error is neither", func(t *testing.T) {
		c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			sendErr(t, w, http.StatusBadGateway, "internal_error", "callback failed")
		})
		_, synced, err := c.Registry().Sync(context.Background(), testAgentURN)
		if synced || !errors.Is(err, &APIError{StatusCode: http.StatusBadGateway}) {
			t.Fatalf("Sync = synced %v, err %v", synced, err)
		}
	})
}

func TestRegistryErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusNotFound, "not_found"},
		{http.StatusBadRequest, "invalid_request"},
		{http.StatusServiceUnavailable, "internal_error"},
	}
	for _, tc := range cases {
		c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			sendErr(t, w, tc.status, tc.code, "boom")
		})
		_, err := c.Registry().Lookup(context.Background(), testAgentURN)
		var api *APIError
		if !errors.As(err, &api) || api.StatusCode != tc.status || api.Code != tc.code || api.Message != "boom" {
			t.Errorf("%d: err = %v", tc.status, err)
		}
		if !errors.Is(err, &APIError{StatusCode: tc.status, Code: tc.code}) {
			t.Errorf("%d: errors.Is against the status/code failed for %v", tc.status, err)
		}
	}
}

func TestRegistryDaemonUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := MustNew(srv.URL)
	srv.Close()
	_, err := c.Registry().Lookup(context.Background(), testAgentURN)
	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("err = %v, want ErrDaemonUnreachable", err)
	}
}
