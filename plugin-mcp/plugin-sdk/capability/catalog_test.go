package capability

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCatalogSelectionAndIsolation(t *testing.T) {
	extension := Descriptor{Name: "host.example.lookup", SchemaVersion: 1, Description: "Example lookup", EffectCeiling: Read, Operations: []string{"lookup"}, ScopeSchema: ScopeSchema{Allowlists: []string{"operations", "targets", "effects"}}}
	c, err := NewCatalog([]string{ReadonlyQuery, StorageRead}, []Descriptor{extension})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lookup(MCPReach, 1); err == nil {
		t.Fatal("unsupported capability visible")
	}
	if _, err := c.Lookup(ReadonlyQuery, 2); err == nil {
		t.Fatal("version mismatch accepted")
	}
	d, _ := c.Lookup(StorageRead, 1)
	if !d.Proposed {
		t.Fatal("proposal presented as ratified")
	}
	extension.Operations[0] = "mutate"
	d, _ = c.Lookup("host.example.lookup", 1)
	if d.Operations[0] != "lookup" {
		t.Fatal("catalog aliases caller")
	}
	d.Operations[0] = "mutate"
	again, _ := c.Lookup(d.Name, 1)
	if again.Operations[0] != "lookup" {
		t.Fatal("lookup aliases catalog")
	}
}
func TestReservedAndMalformedExtensions(t *testing.T) {
	for _, name := range []string{ReadonlyQuery, StorageWrite, "arbitrary.operation", "host..read", "plugin.Example.read", "host.example.*"} {
		_, err := NewCatalog(nil, []Descriptor{{Name: name, SchemaVersion: 1, Description: "Override", EffectCeiling: Read, Operations: []string{"read"}}})
		if err == nil {
			t.Fatalf("accepted extension %s", name)
		}
	}
}
func TestDescriptorScopeChecks(t *testing.T) {
	c, _ := NewCatalog([]string{StorageRead}, nil)
	d, _ := c.Lookup(StorageRead, 1)
	s := Scope{Allowlists: map[string][]string{"operations": {"host/storage/get"}, "targets": {"owner"}, "effects": {"read"}, "keys": {"preferences"}}, Limits: map[string]int64{"response_bytes": 1024}}
	if err := d.ValidateScope(1, s); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(Scope){
		func(s Scope) { s.Allowlists["effects"] = []string{"write"} },
		func(s Scope) { s.Allowlists["effects"] = []string{"unknown"} },
		func(s Scope) { s.Allowlists["operations"] = []string{"host/storage/put"} },
		func(s Scope) { s.Allowlists["unknown"] = []string{"yes"} },
		func(s Scope) { delete(s.Allowlists, "keys") },
		func(s Scope) { s.Limits["unknown"] = 1 },
	} {
		raw, _ := json.Marshal(s)
		var candidate Scope
		json.Unmarshal(raw, &candidate)
		mutation(candidate)
		err := d.ValidateScope(1, candidate)
		var e *Error
		if !errors.As(err, &e) || e.Capability != StorageRead {
			t.Fatalf("scope accepted: %#v (%v)", candidate, err)
		}
	}
}
func TestWireFailureNeverRetries(t *testing.T) {
	e := &Error{Code: UnknownOutcome, RequestID: "request", EffectState: Unknown}
	payload, err := e.RPCData()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	json.Unmarshal(b, &data)
	if data["retryable"] != false || data["effect_state"] != "unknown" || data["contract"] != "host-rpc/1" {
		t.Fatalf("failure: %s", b)
	}
}

func TestExtensionOperationsHaveOneDescriptorOwner(t *testing.T) {
	makeDescriptor := func(name string) Descriptor {
		return Descriptor{Name: name, SchemaVersion: 1, Description: "Private callback", EffectCeiling: Write, Operations: []string{"callback"}, ScopeSchema: ScopeSchema{Allowlists: []string{"operations", "targets", "effects"}}}
	}
	if _, err := NewCatalog(nil, []Descriptor{makeDescriptor("host.one.call"), makeDescriptor("host.two.call")}); err == nil {
		t.Fatal("operation shared by different descriptors")
	}
}
