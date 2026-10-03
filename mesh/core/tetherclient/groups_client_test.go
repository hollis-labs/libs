package tether

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

const (
	testGroupURN  = "msg://agent/agent-mux/grp_01"
	testMemberURN = "msg://agent/agent-mux/agt_02"
)

func TestGroupsCreate(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/groups")
		if body["display_name"] != "Ops" || body["last_updated_by"] != testAgentURN || body["role"] != "team" {
			t.Errorf("body = %v", body)
		}
		sendJSON(t, w, http.StatusCreated, RegistryProfile{URN: testGroupURN, Kind: RegistryKindGroup})
	})
	got, err := c.Groups().Create(context.Background(), CreateGroupRequest{DisplayName: "Ops", Role: "team", CreatorURN: testAgentURN, Capabilities: []string{"ops"}})
	if err != nil || got.URN != testGroupURN || got.Kind != RegistryKindGroup {
		t.Fatalf("Create = %+v, %v", got, err)
	}
}

func TestGroupsLookupAndListForMember(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		switch r.URL.Path {
		case "/groups/" + testGroupURN:
			expect(t, r, http.MethodGet, "/groups/"+testGroupURN)
			sendJSON(t, w, http.StatusOK, RegistryProfile{URN: testGroupURN})
		case "/groups":
			if r.URL.Query().Get("member") != testMemberURN {
				t.Errorf("member = %q", r.URL.Query().Get("member"))
			}
			sendJSON(t, w, http.StatusOK, map[string]any{"groups": []RegistryProfile{{URN: testGroupURN}}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	if got, err := c.Groups().Lookup(context.Background(), testGroupURN); err != nil || got.URN != testGroupURN {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}
	if got, err := c.Groups().ListForMember(context.Background(), testMemberURN); err != nil || len(got) != 1 {
		t.Fatalf("ListForMember = %+v, %v", got, err)
	}
}

func TestGroupsListForMemberEmptyIsNonNil(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		sendJSON(t, w, http.StatusOK, map[string]any{"groups": nil})
	})
	got, err := c.Groups().ListForMember(context.Background(), testMemberURN)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("ListForMember = %#v, %v", got, err)
	}
}

func TestGroupsArchive(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodDelete, "/groups/"+testGroupURN)
		if r.URL.Query().Get("as") != testAgentURN {
			t.Errorf("as = %q", r.URL.Query().Get("as"))
		}
		sendJSON(t, w, http.StatusOK, RegistryProfile{URN: testGroupURN, Status: RegistryStatusArchived})
	})
	got, err := c.Groups().Archive(context.Background(), testGroupURN, testAgentURN)
	if err != nil || got.Status != RegistryStatusArchived {
		t.Fatalf("Archive = %+v, %v", got, err)
	}
}

func TestGroupsMembership(t *testing.T) {
	var seen []string
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		members := "/groups/" + testGroupURN + "/members"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == members:
			if body["member"] != testMemberURN || body["by"] != testAgentURN || body["role"] != "moderator" {
				t.Errorf("add body = %v", body)
			}
			sendJSON(t, w, http.StatusCreated, GroupMember{GroupURN: testGroupURN, MemberURN: testMemberURN, Role: RegistryMemberRoleModerator})
		case r.Method == http.MethodDelete && r.URL.Path == members+"/"+testMemberURN:
			if r.URL.Query().Get("as") != testAgentURN {
				t.Errorf("remove as = %q", r.URL.Query().Get("as"))
			}
			sendJSON(t, w, http.StatusOK, map[string]any{"ok": true})
		case r.Method == http.MethodPatch && r.URL.Path == members+"/"+testMemberURN:
			if body["role"] != "owner" || body["by"] != testAgentURN {
				t.Errorf("set-role body = %v", body)
			}
			sendJSON(t, w, http.StatusOK, map[string]any{"ok": true})
		case r.Method == http.MethodPost && r.URL.Path == "/groups/"+testGroupURN+"/leave":
			if body["member"] != testMemberURN {
				t.Errorf("leave body = %v", body)
			}
			sendJSON(t, w, http.StatusOK, map[string]any{"ok": true})
		case r.Method == http.MethodGet && r.URL.Path == members:
			sendJSON(t, w, http.StatusOK, map[string]any{"members": []GroupMember{{MemberURN: testMemberURN, DisplayName: "Bo"}}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	ctx := context.Background()
	g := c.Groups()
	m, err := g.AddMember(ctx, testGroupURN, testMemberURN, testAgentURN, RegistryMemberRoleModerator)
	if err != nil || m.Role != RegistryMemberRoleModerator {
		t.Fatalf("AddMember = %+v, %v", m, err)
	}
	if err := g.RemoveMember(ctx, testGroupURN, testMemberURN, testAgentURN); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if err := g.SetMemberRole(ctx, testGroupURN, testMemberURN, RegistryMemberRoleOwner, testAgentURN); err != nil {
		t.Fatalf("SetMemberRole: %v", err)
	}
	if err := g.Leave(ctx, testGroupURN, testMemberURN); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	list, err := g.ListMembers(ctx, testGroupURN)
	if err != nil || len(list) != 1 || list[0].DisplayName != "Bo" {
		t.Fatalf("ListMembers = %+v, %v", list, err)
	}
	if len(seen) != 5 {
		t.Errorf("requests = %v", seen)
	}
}

func TestGroupsListMembersEmptyIsNonNil(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		sendJSON(t, w, http.StatusOK, map[string]any{"members": nil})
	})
	got, err := c.Groups().ListMembers(context.Background(), testGroupURN)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("ListMembers = %#v, %v", got, err)
	}
}

func TestGroupsSend(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/groups/"+testGroupURN+"/messages")
		if body["from"] != testAgentURN || body["content_type"] != "text/plain" || body["thread_id"] != "t1" {
			t.Errorf("body = %v", body)
		}
		if body["payload"] != "hi @bo" {
			t.Errorf("payload = %v, want the raw JSON value passed through", body["payload"])
		}
		sendJSON(t, w, http.StatusCreated, SendGroupResult{MessageID: "m1", GroupSeq: 7, FanoutError: "no inbox"})
	})
	got, err := c.Groups().Send(context.Background(), testGroupURN, SendGroupRequest{
		From: testAgentURN, ThreadID: "t1", ContentType: "text/plain", Payload: json.RawMessage(`"hi @bo"`),
	})
	if err != nil || got.MessageID != "m1" || got.GroupSeq != 7 || got.FanoutError != "no inbox" {
		t.Fatalf("Send = %+v, %v", got, err)
	}
}

func TestGroupsListMessages(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/groups/"+testGroupURN+"/messages")
		q := r.URL.Query()
		if q.Get("as") != testMemberURN || q.Get("since_seq") != "4" || q.Get("thread_id") != "t1" || q.Get("limit") != "10" {
			t.Errorf("query = %v", q)
		}
		sendJSON(t, w, http.StatusOK, ListGroupMessagesResult{Messages: []GroupMessage{{ID: "m1", GroupSeq: 5, FromURN: testAgentURN, Payload: json.RawMessage(`{"a":1}`)}}, NextSeq: 6})
	})
	got, err := c.Groups().ListMessages(context.Background(), testGroupURN, ListMessagesParams{As: testMemberURN, SinceSeq: 4, ThreadID: "t1", Limit: 10})
	if err != nil || len(got.Messages) != 1 || got.NextSeq != 6 || string(got.Messages[0].Payload) != `{"a":1}` {
		t.Fatalf("ListMessages = %+v, %v", got, err)
	}
}

func TestGroupsListMessagesOmitsZeroFilters(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		q := r.URL.Query()
		if q.Has("since_seq") || q.Has("thread_id") || q.Has("limit") || q.Get("as") != testMemberURN {
			t.Errorf("query = %v", q)
		}
		sendJSON(t, w, http.StatusOK, map[string]any{"messages": nil, "next_seq": 0})
	})
	got, err := c.Groups().ListMessages(context.Background(), testGroupURN, ListMessagesParams{As: testMemberURN})
	if err != nil || got.Messages == nil || len(got.Messages) != 0 {
		t.Fatalf("ListMessages = %#v, %v; want a non-nil empty slice", got, err)
	}
}

func TestGroupsMarkRead(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		expect(t, r, http.MethodPost, "/groups/"+testGroupURN+"/read")
		if body["as"] != testMemberURN || body["up_to_seq"] != float64(9) {
			t.Errorf("body = %v", body)
		}
		sendJSON(t, w, http.StatusOK, map[string]any{"ok": true})
	})
	if err := c.Groups().MarkRead(context.Background(), testGroupURN, testMemberURN, 9); err != nil {
		t.Fatal(err)
	}
}

func TestGroupsMentions(t *testing.T) {
	since := time.Date(2026, 9, 30, 12, 0, 0, 0, time.FixedZone("x", 3600))
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		expect(t, r, http.MethodGet, "/mentions")
		q := r.URL.Query()
		if q.Get("as") != testMemberURN || q.Get("since") != "2026-09-30T11:00:00Z" || q.Get("limit") != "3" {
			t.Errorf("query = %v (since must be UTC RFC3339)", q)
		}
		sendJSON(t, w, http.StatusOK, map[string]any{"mentions": []GroupMessage{{ID: "m1", GroupURN: testGroupURN}}})
	})
	got, err := c.Groups().Mentions(context.Background(), MentionsParams{As: testMemberURN, Since: since, Limit: 3})
	if err != nil || len(got) != 1 || got[0].GroupURN != testGroupURN {
		t.Fatalf("Mentions = %+v, %v", got, err)
	}
}

func TestGroupsMentionsEmptyIsNonNil(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Has("since") || r.URL.Query().Has("limit") {
			t.Errorf("zero filters must be omitted: %v", r.URL.Query())
		}
		sendJSON(t, w, http.StatusOK, map[string]any{"mentions": nil})
	})
	got, err := c.Groups().Mentions(context.Background(), MentionsParams{As: testMemberURN})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("Mentions = %#v, %v", got, err)
	}
}

func TestGroupsErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusNotFound, "not_found"},
		{http.StatusBadRequest, "invalid_request"},
		{http.StatusForbidden, "forbidden"},
		{http.StatusLocked, "locked"},
	}
	for _, tc := range cases {
		c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			sendErr(t, w, tc.status, tc.code, "nope")
		})
		_, err := c.Groups().Send(context.Background(), testGroupURN, SendGroupRequest{From: testAgentURN})
		var api *APIError
		if !errors.As(err, &api) || api.StatusCode != tc.status || api.Code != tc.code || api.Message != "nope" {
			t.Errorf("%d: err = %v", tc.status, err)
		}
		var amb *GroupAmbiguousMentionError
		if errors.As(err, &amb) {
			t.Errorf("%d: a plain error must not be an ambiguous-mention error", tc.status)
		}
	}
}

func TestGroupsSendAmbiguousMention(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		sendJSON(t, w, http.StatusBadRequest, map[string]any{
			"error":      ErrorDetail{Code: "invalid_request", Message: "ambiguous mention"},
			"token":      "bo",
			"candidates": []string{"msg://agent/agent-mux/agt_a", "msg://agent/agent-mux/agt_b"},
		})
	})
	_, err := c.Groups().Send(context.Background(), testGroupURN, SendGroupRequest{From: testAgentURN, Payload: json.RawMessage(`"@bo"`)})
	var amb *GroupAmbiguousMentionError
	if !errors.As(err, &amb) || amb.Token != "bo" || len(amb.Candidates) != 2 || amb.Candidates[1] != "msg://agent/agent-mux/agt_b" {
		t.Fatalf("err = %#v, want *GroupAmbiguousMentionError with both candidates", err)
	}
	if !errors.Is(err, &APIError{StatusCode: http.StatusBadRequest, Code: "invalid_request"}) {
		t.Error("an ambiguous mention is still a 400 invalid_request to errors.Is")
	}
	if errors.Is(err, &APIError{StatusCode: http.StatusNotFound}) {
		t.Error("an ambiguous mention must not match a 404")
	}
}

func TestGroupsBadRequestWithoutCandidatesIsAPIError(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		sendJSON(t, w, http.StatusBadRequest, map[string]any{
			"error": ErrorDetail{Code: "invalid_request", Message: "from required"}, "token": "x", "candidates": []string{},
		})
	})
	_, err := c.Groups().Send(context.Background(), testGroupURN, SendGroupRequest{})
	var amb *GroupAmbiguousMentionError
	var api *APIError
	if errors.As(err, &amb) || !errors.As(err, &api) || api.Message != "from required" {
		t.Fatalf("err = %#v", err)
	}
}

func TestGroupsErrorWithNonJSONBody(t *testing.T) {
	c := regServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("kaboom"))
	})
	_, err := c.Groups().Lookup(context.Background(), testGroupURN)
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != 500 || api.Body != "kaboom" {
		t.Fatalf("err = %#v", err)
	}
}
