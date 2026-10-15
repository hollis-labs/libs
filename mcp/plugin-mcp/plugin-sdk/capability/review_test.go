package capability

import (
	"reflect"
	"testing"
)

func validExtension() Descriptor {
	return Descriptor{Name: "host.example.lookup", SchemaVersion: 1, Description: "Lookup", EffectCeiling: Read, Operations: []string{"example/lookup"}, ScopeSchema: ScopeSchema{Allowlists: []string{"operations", "targets", "effects"}, Limits: []string{"bytes"}}}
}
func TestExtensionNameAndOperationIsolation(t *testing.T) {
	for _, name := range []string{ReadonlyQuery, StorageWrite, "arbitrary.operation", "host..read", "plugin.Example.read", "host.example.*"} {
		d := validExtension()
		d.Name = name
		if _, err := NewCatalog(nil, []Descriptor{d}); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	for _, op := range []string{"host/secrets/get", "host/storage/put", "host/mcp/call_tool", "host/future/shared", "host/*", " host/readonly/query ", "example/*", "example/?", "example/[read]", "example/read\x00", "example/read\n", " example/read"} {
		d := validExtension()
		d.Operations = []string{op}
		if _, err := NewCatalog(nil, []Descriptor{d}); err == nil {
			t.Fatalf("accepted operation %q", op)
		}
	}
	if _, err := NewCatalog(nil, []Descriptor{validExtension()}); err != nil {
		t.Fatal(err)
	}
}
func TestExtensionEffectAndDuplicateSupport(t *testing.T) {
	d := validExtension()
	d.EffectCeiling = "unknown"
	if _, err := NewCatalog(nil, []Descriptor{d}); err == nil {
		t.Fatal("unknown effect ceiling accepted")
	}
	if _, err := NewCatalog([]string{StorageRead, StorageRead}, nil); err == nil {
		t.Fatal("duplicate supported descriptor accepted")
	}
}
func TestDescriptorSchemaCopyIsolation(t *testing.T) {
	input := validExtension()
	c, err := NewCatalog(nil, []Descriptor{input})
	if err != nil {
		t.Fatal(err)
	}
	input.ScopeSchema.Allowlists[0] = "other"
	input.ScopeSchema.Limits[0] = "other"
	d, _ := c.Lookup(input.Name, 1)
	if d.ScopeSchema.Allowlists[0] != "operations" || d.ScopeSchema.Limits[0] != "bytes" {
		t.Fatal("catalog retains input schema")
	}
	d.ScopeSchema.Allowlists[0] = "other"
	d.ScopeSchema.Limits[0] = "other"
	d, _ = c.Lookup(input.Name, 1)
	if d.ScopeSchema.Allowlists[0] != "operations" || d.ScopeSchema.Limits[0] != "bytes" {
		t.Fatal("lookup exposes internal schema")
	}
}
func TestIntersectionCopiesEveryInput(t *testing.T) {
	inputs := []Scope{
		{Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": 40}},
		{Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": 30}},
		{Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": 20}},
		{Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": 10}},
	}
	got, err := Intersect(StorageRead, inputs[0], inputs[1], inputs[2], inputs[3])
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits["bytes"] != 10 {
		t.Fatal("wrong intersection")
	}
	got.Limits["bytes"] = 999
	got.Allowlists["targets"][0] = "other"
	for i, input := range inputs {
		if input.Limits["bytes"] != int64(40-i*10) || !reflect.DeepEqual(input.Allowlists["targets"], []string{"one"}) {
			t.Fatalf("input %d mutated", i)
		}
	}
}
func TestIntersectionValidatesEachInput(t *testing.T) {
	good := Scope{Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": 10}}
	bad := Scope{Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": -1}}
	for i := range 4 {
		inputs := []Scope{good, good, good, good}
		inputs[i] = bad
		if _, err := Intersect(StorageRead, inputs[0], inputs[1], inputs[2], inputs[3]); err == nil {
			t.Fatalf("input %d unchecked", i)
		}
	}
}
func TestNewDimensionAndZeroBudget(t *testing.T) {
	previous := Scope{Allowlists: map[string][]string{"targets": {"one"}}, Limits: map[string]int64{"bytes": 1}}
	next := Scope{Allowlists: map[string][]string{"targets": {"one"}, "secrets": {"secret"}}, Limits: map[string]int64{"bytes": 1}}
	if err := CheckNarrowing(StorageRead, previous, next); err == nil {
		t.Fatal("new dimension authorized")
	}
	for _, s := range []Scope{{}, {Limits: map[string]int64{"bytes": 0}}} {
		if s.Within("bytes", 0) {
			t.Fatal("zero/missing ceiling grants authority")
		}
	}
	if !previous.Within("bytes", 0) || !previous.Within("bytes", 1) || previous.Within("bytes", 2) || previous.Within("bytes", -1) {
		t.Fatal("positive ceiling boundary")
	}
}
func TestDescriptorVersionAndMissingLimit(t *testing.T) {
	c, _ := NewCatalog([]string{StorageRead}, nil)
	d, _ := c.Lookup(StorageRead, 1)
	s := Scope{Allowlists: map[string][]string{"operations": {"host/storage/get"}, "targets": {"owner"}, "effects": {"read"}, "keys": {"one"}}, Limits: map[string]int64{"response_bytes": 10}}
	if err := d.ValidateScope(1, s); err != nil {
		t.Fatal(err)
	}
	if err := d.ValidateScope(2, s); err == nil {
		t.Fatal("scope version unchecked")
	}
	delete(s.Limits, "response_bytes")
	if err := d.ValidateScope(1, s); err == nil {
		t.Fatal("missing limit accepted")
	}
}
func TestRPCDataValidationAndDetails(t *testing.T) {
	for _, e := range []*Error{
		{Code: InternalError, EffectState: "invalid"},
		{Code: UnknownOutcome, EffectState: NotStarted},
		{Code: UnknownOutcome, EffectState: NotCommitted},
		{Code: UnknownOutcome, EffectState: Committed},
		{Code: "unknown", EffectState: Unknown},
		{Code: InternalError, EffectState: Unknown, Detail: "raw secret detail"},
	} {
		e.RequestID = 1
		if _, err := e.RPCData(); err == nil {
			t.Fatalf("accepted %#v", e)
		}
	}
	d, err := (&Error{Code: InternalError, RequestID: 1}).RPCData()
	if err != nil || d.EffectState != Unknown {
		t.Fatal("missing state did not default unknown")
	}
	for _, detail := range []FailureDetail{StaleBinding, CallbackCycle, DepthExceeded, ParentInvalid, ParentTerminal} {
		e := &Error{Code: Conflict, EffectState: NotCommitted, Detail: detail, RequestID: 1}
		d, err := e.RPCData()
		if err != nil || d.Detail != detail || d.Code != Conflict || d.Retryable {
			t.Fatalf("detail: %#v %v", d, err)
		}
	}
}
func TestScopeIdentifiersAreCanonical(t *testing.T) {
	for _, id := range []string{" padded", "padded ", "bad\x00", "bad\n"} {
		s := Scope{Allowlists: map[string][]string{"targets": {id}}}
		if err := s.Validate(StorageRead); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
