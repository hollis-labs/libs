package capability

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRenewalPreservesHostScopeAndAuthority(t *testing.T) {
	now := time.Now().UTC()
	owner := RuntimeIdentity{"host", "plugin", 1}
	old := GrantSet{{GrantID: "query", Name: "host.nanite.readonly.query", SchemaVersion: 1, Scope: json.RawMessage(`{"resources":["sessions"], "all_sessions":true}`), HostInstance: owner.HostInstance, OwnerID: owner.OwnerID, OwnerGeneration: owner.OwnerGeneration, Audience: "nanite", IssuedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), PolicyRevision: "approved"}}
	next := old.Clone()
	next[0].IssuedAt = now.Format(time.RFC3339Nano)
	next[0].ExpiresAt = now.Add(24 * time.Hour).Format(time.RFC3339Nano)
	if err := old.ValidateRenewal(next, owner, now); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(GrantSet)
	}{
		{"scope", func(g GrantSet) { g[0].Scope = json.RawMessage(`{"resources":["sessions"],"all_sessions":true}`) }},
		{"name", func(g GrantSet) { g[0].Name = "readonly.query" }},
		{"schema", func(g GrantSet) { g[0].SchemaVersion++ }},
		{"audience", func(g GrantSet) { g[0].Audience = "other" }},
		{"revision", func(g GrantSet) { g[0].PolicyRevision = "other" }},
		{"id", func(g GrantSet) { g[0].GrantID = "other" }},
		{"runtime", func(g GrantSet) { g[0].OwnerGeneration++ }},
		{"nonadvancing", func(g GrantSet) { g[0].ExpiresAt = old[0].ExpiresAt }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := next.Clone()
			tt.change(g)
			if old.ValidateRenewal(g, owner, now) == nil {
				t.Fatal("changed authority renewed")
			}
		})
	}
	if old.ValidateRenewal(next, owner, now.Add(2*time.Hour)) == nil {
		t.Fatal("expired grant revived")
	}
	copy := old.Clone()
	copy[0].Scope[0] = 'x'
	if old[0].Scope[0] != '{' {
		t.Fatal("clone aliases scope")
	}
}
