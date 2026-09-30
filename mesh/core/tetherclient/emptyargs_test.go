package tether

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An empty id would become an empty path segment and so a request to the
// neighbouring collection route; the methods refuse it without touching the wire.
func TestEmptyIDsAreRefusedBeforeTheWire(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	g := c.Groups()
	calls := map[string]func() error{
		"Groups.Lookup":              func() error { _, e := g.Lookup(ctx, ""); return e },
		"Groups.ListForMember":       func() error { _, e := g.ListForMember(ctx, ""); return e },
		"Groups.Archive":             func() error { _, e := g.Archive(ctx, "", "u"); return e },
		"Groups.AddMember/grp":       func() error { _, e := g.AddMember(ctx, "", "m", "u", ""); return e },
		"Groups.AddMember/mem":       func() error { _, e := g.AddMember(ctx, "g", "", "u", ""); return e },
		"Groups.RemoveMember":        func() error { return g.RemoveMember(ctx, "", "m", "u") },
		"Groups.Leave":               func() error { return g.Leave(ctx, "g", "") },
		"Groups.SetMemberRole":       func() error { return g.SetMemberRole(ctx, "", "m", "", "u") },
		"Groups.ListMembers":         func() error { _, e := g.ListMembers(ctx, ""); return e },
		"Groups.Send":                func() error { _, e := g.Send(ctx, "", SendGroupRequest{}); return e },
		"Groups.ListMessages":        func() error { _, e := g.ListMessages(ctx, "", ListMessagesParams{}); return e },
		"Groups.MarkRead":            func() error { return g.MarkRead(ctx, "", "m", 1) },
		"GetWorkstream":              func() error { _, e := c.GetWorkstream(ctx, ""); return e },
		"AssignSessionWorkstream":    func() error { return c.AssignSessionWorkstream(ctx, "", "w") },
		"EnsureSessionWorkstream":    func() error { _, e := c.EnsureSessionWorkstream(ctx, "", "n", ""); return e },
		"SessionWorkstreamNamespace": func() error { _, e := c.SessionWorkstreamNamespace(ctx, ""); return e },
		"SessionDigest":              func() error { _, e := c.SessionDigest(ctx, "", DigestQuery{}); return e },
		"WorkstreamDigest":           func() error { _, e := c.WorkstreamDigest(ctx, "", DigestQuery{}); return e },
	}
	for name, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Errorf("%s with an empty id: %v", name, err)
		}
	}
	if hits != 0 {
		t.Errorf("an empty id reached the daemon %d times", hits)
	}
}
