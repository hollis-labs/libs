package effects

import "testing"

func TestHeaderBindingAndEvidenceIsolation(t *testing.T) {
	valid := Header{Version: SchemaVersion, OperationID: "operation", InputDigest: "digest"}
	if !valid.Valid() {
		t.Fatal("valid header rejected")
	}
	for _, h := range []Header{{}, {Version: "future", OperationID: "operation", InputDigest: "digest"}, {Version: SchemaVersion, InputDigest: "digest"}, {Version: SchemaVersion, OperationID: "operation"}} {
		if h.Valid() {
			t.Fatal("incomplete header accepted")
		}
	}
	e := Evidence{Header: valid, Links: []LinkEvidence{{Destination: "auth.json"}}}
	c := e.Clone()
	c.Links[0].Destination = "changed"
	if e.Links[0].Destination != "auth.json" {
		t.Fatal("evidence aliased")
	}
}
