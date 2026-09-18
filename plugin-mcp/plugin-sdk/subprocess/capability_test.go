package subprocess

import (
	"encoding/json"
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

func TestInitParamsRoundtripGranted(t *testing.T) {
	in := InitParams{
		PluginDir: "/plugins/foo",
		HostInfo:  HostInfo{Version: "1.2.3", Protocol: ProtocolVersion},
		Granted:   []string{"alpha", "beta"},
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out InitParams
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(out.Granted) != 2 || out.Granted[0] != "alpha" || out.Granted[1] != "beta" {
		t.Errorf("Granted = %v, want [alpha beta]", out.Granted)
	}
	if !out.HasCapability("alpha") || !out.HasCapability("beta") {
		t.Errorf("HasCapability missed a granted name: %v", out.Granted)
	}
	if out.HasCapability("gamma") {
		t.Errorf("HasCapability(gamma) = true, want false")
	}
}

// A host on this SDK that grants nothing must produce the same init
// payload it produced before the field existed, so a plugin that never
// heard of capabilities sees an unchanged wire.
func TestInitParamsGrantedOmittedWhenUnused(t *testing.T) {
	b, err := json.Marshal(InitParams{PluginDir: "/plugins/foo"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := fields["granted"]; ok {
		t.Errorf("granted present in %s, want omitted", string(b))
	}
}

// An older host sends no granted at all. A newer plugin must decode it
// cleanly and read the absence as "the host said nothing" — never as a
// decode failure, and never as a grant.
func TestInitParamsForwardCompatNoGranted(t *testing.T) {
	raw := `{
		"plugin_dir": "/plugins/foo",
		"data_dir": "/data/foo",
		"cache_dir": "/cache/foo",
		"config": {"k": "v"},
		"log_level": "info",
		"host_info": {"version": "1.1.0", "protocol": 1}
	}`

	var out InitParams
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.Granted != nil {
		t.Errorf("Granted = %v, want nil", out.Granted)
	}
	if out.HasCapability("anything") {
		t.Errorf("HasCapability = true against a host that sent no granted")
	}
	// Everything the older host did send must still arrive.
	if out.PluginDir != "/plugins/foo" || out.DataDir != "/data/foo" || out.LogLevel != "info" {
		t.Errorf("older payload lost fields: %+v", out)
	}
}

// The mirror case: an older *plugin*, built against an InitParams that
// has no Granted field, receives init params from a newer host that
// sends one. It must decode cleanly and keep every field it knows.
//
// initParamsV040 is the shape of InitParams as of v0.4.0, before
// capability declaration. Do not add fields to it.
type initParamsV040 struct {
	PluginDir string            `json:"plugin_dir"`
	DataDir   string            `json:"data_dir"`
	CacheDir  string            `json:"cache_dir"`
	Config    map[string]string `json:"config"`
	LogLevel  string            `json:"log_level"`
	HostInfo  HostInfo          `json:"host_info"`
}

func TestInitParamsBackCompatOlderPluginIgnoresGranted(t *testing.T) {
	newer := InitParams{
		PluginDir: "/plugins/foo",
		DataDir:   "/data/foo",
		CacheDir:  "/cache/foo",
		Config:    map[string]string{"k": "v"},
		LogLevel:  "debug",
		HostInfo:  HostInfo{Version: "9.9.9", Protocol: ProtocolVersion},
		Granted:   []string{"alpha"},
	}

	b, err := json.Marshal(newer)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var old initParamsV040
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatalf("older plugin failed to decode newer init params: %v", err)
	}

	if old.PluginDir != newer.PluginDir || old.DataDir != newer.DataDir ||
		old.CacheDir != newer.CacheDir || old.LogLevel != newer.LogLevel {
		t.Errorf("older plugin lost fields: %+v", old)
	}
	if old.Config["k"] != "v" {
		t.Errorf("Config = %v, want k=v", old.Config)
	}
	if old.HostInfo.Protocol != ProtocolVersion {
		t.Errorf("HostInfo.Protocol = %d, want %d", old.HostInfo.Protocol, ProtocolVersion)
	}
}

// Capability declaration is additive. It must not move the negotiated
// protocol version, which TestProtocolVersionLockedAt1 also pins.
func TestCapabilityDeclarationDoesNotBumpProtocol(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1: capability declaration is additive", ProtocolVersion)
	}
}

func TestHasCapabilityOnEmptyGrant(t *testing.T) {
	// A host that explicitly grants nothing and a host that has never
	// heard of capabilities are indistinguishable here, by design.
	for name, p := range map[string]InitParams{
		"nil":   {},
		"empty": {Granted: []string{}},
	} {
		if p.HasCapability("alpha") {
			t.Errorf("%s: HasCapability = true, want false", name)
		}
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
