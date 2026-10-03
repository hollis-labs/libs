package capability

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func validGrant() Grant {
	return Grant{GrantID: "g-1", Name: "readonly.query", SchemaVersion: 1, Scope: json.RawMessage(`{"limit":9007199254740993,"nested":[null]}`), HostInstance: "epoch-1", OwnerID: "example.plugin", OwnerGeneration: 1, Audience: "host.private-stdio", IssuedAt: "2026-10-03T20:00:00.000000001Z", ExpiresAt: "2026-10-03T20:00:00.000000002Z", PolicyRevision: "policy-1"}
}

func TestGrantRoundTrip(t *testing.T) {
	g := validGrant()
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var out Grant
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, out) {
		t.Fatalf("lost fields: %s", b)
	}
	if !bytes.Contains(out.Scope, []byte("9007199254740993")) {
		t.Fatal("opaque integer rounded")
	}
	r := RuntimeIdentity{g.HostInstance, g.OwnerID, MaxSafeInteger}
	b, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RuntimeIdentity
	if err := json.Unmarshal(b, &decoded); err != nil || decoded != r {
		t.Fatalf("runtime round trip: %v", err)
	}
}

func TestSharedGrantFixtures(t *testing.T) {
	b, err := os.ReadFile("../protocol/v2/fixtures/grants.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Kind, Input string
		Valid             bool
	}
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var target any
			switch f.Kind {
			case "grant":
				target = &Grant{}
			case "runtime":
				target = &RuntimeIdentity{}
			case "grants":
				target = &GrantSet{}
			default:
				t.Fatal("unknown fixture kind")
			}
			err := json.Unmarshal([]byte(f.Input), target)
			if (err == nil) != f.Valid {
				t.Fatalf("valid=%v, err=%v", f.Valid, err)
			}
			if !f.Valid {
				var typed *GrantValidationError
				if !errors.As(err, &typed) {
					t.Fatalf("untyped failure: %v", err)
				}
			}
		})
	}
}

func TestGrantSetIdentityAndEmptyEncoding(t *testing.T) {
	for _, s := range []GrantSet{nil, {}} {
		b, err := json.Marshal(s)
		if err != nil || string(b) != "[]" {
			t.Fatalf("empty=%s, err=%v", b, err)
		}
	}
	g := validGrant()
	r := RuntimeIdentity{g.HostInstance, g.OwnerID, g.OwnerGeneration}
	if err := (GrantSet{g}).ValidateForRuntime(r); err != nil {
		t.Fatal(err)
	}
	if err := (GrantSet{}).ValidateForRuntime(RuntimeIdentity{}); err == nil {
		t.Fatal("empty grants erased invalid runtime")
	}
	r.OwnerGeneration++
	if err := (GrantSet{g}).ValidateForRuntime(r); err == nil {
		t.Fatal("foreign incarnation accepted")
	}
	if _, err := json.Marshal(GrantSet{g, g}); err == nil {
		t.Fatal("duplicate grant ID encoded")
	}
	g2 := g
	g2.GrantID = "g-2"
	if err := (GrantSet{g, g2}).Validate(); err != nil {
		t.Fatalf("distinct grants for same name refused: %v", err)
	}
}

func TestFailedDecodeDoesNotReplaceReceiver(t *testing.T) {
	g := validGrant()
	before := g
	if err := json.Unmarshal([]byte(`{"grant_id":"secret"}`), &g); err == nil {
		t.Fatal("missing fields accepted")
	}
	if !reflect.DeepEqual(g, before) {
		t.Fatal("failed decode mutated grant")
	}
	s := GrantSet{g}
	if err := json.Unmarshal([]byte(`[{}]`), &s); err == nil || !reflect.DeepEqual(s, GrantSet{g}) {
		t.Fatal("failed decode replaced grant set")
	}
	r := RuntimeIdentity{g.HostInstance, g.OwnerID, 1}
	if err := json.Unmarshal([]byte(`{"host_instance":"wrong"}`), &r); err == nil || r.HostInstance != g.HostInstance {
		t.Fatal("failed decode replaced tuple")
	}
}

func TestInvalidProducerValuesRefused(t *testing.T) {
	for _, mutate := range []func(*Grant){
		func(g *Grant) { g.OwnerGeneration = MaxSafeInteger + 1 }, func(g *Grant) { g.Scope = nil }, func(g *Grant) { g.Scope = json.RawMessage(`null`) }, func(g *Grant) { g.Scope = json.RawMessage(`{"x":1,"x":2}`) }, func(g *Grant) { g.ExpiresAt = g.IssuedAt }, func(g *Grant) { g.SchemaVersion = 0 }, func(g *Grant) { g.PolicyRevision = " " },
	} {
		g := validGrant()
		mutate(&g)
		if _, err := json.Marshal(g); err == nil {
			t.Fatal("invalid producer grant encoded")
		}
	}
}
