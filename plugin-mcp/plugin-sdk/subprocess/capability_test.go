package subprocess

import (
	"encoding/json"
	"github.com/hollis-labs/plugin-sdk/capability"
	"testing"
)

func TestCapabilityRequestRoundtripAllFields(t *testing.T) {
	in := CapabilityRequest{
		Name:     "example.host.vocabulary",
		Reason:   "needed to reach the thing this plugin wraps",
		Optional: true,
		Metadata: json.RawMessage(`{"scope":"read"}`),
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out CapabilityRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.Name != in.Name {
		t.Errorf("Name = %q, want %q", out.Name, in.Name)
	}
	if out.Reason != in.Reason {
		t.Errorf("Reason = %q, want %q", out.Reason, in.Reason)
	}
	if !out.Optional {
		t.Errorf("Optional = false, want true")
	}
	if string(out.Metadata) != `{"scope":"read"}` {
		t.Errorf("Metadata = %q, want %q", string(out.Metadata), `{"scope":"read"}`)
	}
}

// Metadata is opaque to the SDK: whatever JSON a host puts there must
// survive a roundtrip untouched, including shapes this module has no
// type for.
func TestCapabilityRequestMetadataIsOpaque(t *testing.T) {
	raw := `{"nested":{"a":[1,2,{"b":null}]},"n":3.5}`
	in := CapabilityRequest{Name: "x", Metadata: json.RawMessage(raw)}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out CapabilityRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var got, want any
	if err := json.Unmarshal(out.Metadata, &got); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if gotJSON, wantJSON := mustMarshal(t, got), mustMarshal(t, want); gotJSON != wantJSON {
		t.Errorf("metadata changed across roundtrip:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

// A minimal request — name only — must not emit the optional fields, so
// a host reading it with a stricter schema sees only what was declared.
func TestCapabilityRequestMinimalOmitsOptionalFields(t *testing.T) {
	b, err := json.Marshal(CapabilityRequest{Name: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"name":"x"}` {
		t.Errorf("marshaled to %s, want %s", string(b), `{"name":"x"}`)
	}
}

func TestHasCapabilityUsesGrantNames(t *testing.T) {
	p := InitParams{Grants: capability.GrantSet{{Name: "alpha"}, {Name: "beta"}}}
	if !p.HasCapability("alpha") || !p.HasCapability("beta") || p.HasCapability("gamma") {
		t.Fatal("grant name lookup")
	}
	if (&InitParams{}).HasCapability("alpha") {
		t.Fatal("empty grants")
	}
}
func TestEmptyGrantsAreExplicit(t *testing.T) {
	p := validInitParams()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mustMarshal(t, p)), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["grants"]) != "[]" {
		t.Fatal("empty grants omitted")
	}
	delete(fields, "grants")
	raw, _ := json.Marshal(fields)
	if json.Unmarshal(raw, &p) == nil {
		t.Fatal("missing grants accepted")
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
