package subprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestHostRPCSharedStructuralFixtures(t *testing.T) {
	data, err := os.ReadFile("../protocol/v2/fixtures/host-rpc.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Structural []struct {
			Name, Schema, Raw string
			Valid             bool
		}
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	constructors := map[string]func() any{
		"ParentCall":          func() any { return &ParentCall{} },
		"ForwardContext":      func() any { return &ForwardContext{} },
		"ReverseContext":      func() any { return &ReverseContext{} },
		"Header":              func() any { return &HostRPCHeader{} },
		"LogField":            func() any { return &HostRPCLogField{} },
		"RemainingBudgets":    func() any { return &HostRPCRemainingBudgets{} },
		"StorageGetParams":    func() any { return &StorageGetParams{} },
		"StorageGetResult":    func() any { return &StorageGetResult{} },
		"StoragePutParams":    func() any { return &StoragePutParams{} },
		"StoragePutResult":    func() any { return &StoragePutResult{} },
		"StorageDeleteParams": func() any { return &StorageDeleteParams{} },
		"StorageDeleteResult": func() any { return &StorageDeleteResult{} },
		"SecretsGetParams":    func() any { return &SecretsGetParams{} },
		"SecretsGetResult":    func() any { return &SecretsGetResult{} },
		"EgressRequestParams": func() any { return &EgressRequestParams{} },
		"EgressRequestResult": func() any { return &EgressRequestResult{} },
		"EventsPublishParams": func() any { return &EventsPublishParams{} },
		"EventsPublishResult": func() any { return &EventsPublishResult{} },
		"LogParams":           func() any { return &LogParams{} },
		"LogResult":           func() any { return &LogResult{} },
		"ReadonlyQueryParams": func() any { return &ReadonlyQueryParams{} },
		"ReadonlyQueryResult": func() any { return &ReadonlyQueryResult{} },
		"MCPListToolsParams":  func() any { return &MCPListToolsParams{} },
		"MCPListToolsResult":  func() any { return &MCPListToolsResult{} },
		"MCPCallToolParams":   func() any { return &MCPCallToolParams{} },
		"MCPCallToolResult":   func() any { return &MCPCallToolResult{} },
		"MCPCancelCallParams": func() any { return &MCPCancelCallParams{} },
		"MCPCancelCallResult": func() any { return &MCPCancelCallResult{} },
		"BindingsRenewParams": func() any { return &BindingsRenewParams{} },
		"BindingsRenewResult": func() any { return &BindingsRenewResult{} },
		"MCPTool":             func() any { return &MCPTool{} },
	}
	for _, v := range corpus.Structural {
		t.Run(v.Name, func(t *testing.T) {
			if v.Schema == "ApplicationErrorResponse" || v.Schema == "HostRPCErrorData" {
				t.Skip("application error DTO waits for shared capability leaf update")
			}
			err := ValidateHostRPCDTO(v.Schema, []byte(v.Raw))
			if (err == nil) != v.Valid {
				t.Fatalf("valid=%v, error=%v", v.Valid, err)
			}
			if !v.Valid {
				var typed *HostRPCValidationError
				if !errors.As(err, &typed) {
					t.Fatalf("unclassified error: %v", err)
				}
				return
			}
			constructor, ok := constructors[v.Schema]
			if !ok {
				return
			} // Base64 scalar is validated above.
			target := constructor()
			if err := json.Unmarshal([]byte(v.Raw), target); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateHostRPCDTO(v.Schema, encoded); err != nil {
				t.Fatal(err)
			}
			again := constructor()
			if err := json.Unmarshal(encoded, again); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(target, again) {
				t.Fatal("typed roundtrip changed presence/value")
			}
		})
	}
	// Policy scenarios intentionally require a future fake host and are not replayed.
}
func TestHostRPCStrictContexts(t *testing.T) {
	for _, raw := range []string{
		`{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1}}`,
		`{"binding_id":"b","timeout_ms":1e0,"parent_call":{"request_owner":"host","id":1}}`,
		`{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1.0}}`,
		`{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":9007199254740992}}`,
		`{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1,"id":2}}`,
		`{"binding_id":"\ud800","timeout_ms":1,"parent_call":{"request_owner":"host","id":1}}`,
	} {
		var v ReverseContext
		err := json.Unmarshal([]byte(raw), &v)
		if (err == nil) != (raw == `{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1}}`) {
			t.Errorf("unexpected classification: %v", err)
		}
	}
	for _, raw := range []string{`{"timeout_ms":1}`, `{"binding_id":"b","timeout_ms":1}`} {
		var v ForwardContext
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"timeout_ms":1,"parent_call":{"request_owner":"host","id":1}}`, `{"timeout_ms":1,"binding_id":null}`, `{"timeout_ms":1,"Timeout_ms":1}`} {
		var v ForwardContext
		if json.Unmarshal([]byte(raw), &v) == nil {
			t.Fatal("invalid forward context accepted")
		}
	}
}
func TestHostRPCDecodeFailurePreservesReceiver(t *testing.T) {
	v := StorageGetResult{Found: false}
	before := v
	if json.Unmarshal([]byte(`{"found":true}`), &v) == nil {
		t.Fatal("missing found payload accepted")
	}
	if !reflect.DeepEqual(v, before) {
		t.Fatal("failed decode changed receiver")
	}
}
func TestHostRPCProducerValidation(t *testing.T) {
	bad := "\xff"
	if _, err := json.Marshal(HostRPCHeader{Name: "X-Test", Value: bad}); err == nil {
		t.Fatal("invalid UTF-8 producer string normalized")
	}
	if _, err := json.Marshal(StorageGetResult{Found: true}); err == nil {
		t.Fatal("missing required result content")
	}
	key := "r1"
	if _, err := json.Marshal(StorageGetResult{Revision: &key}); err == nil {
		t.Fatal("absent result exposes revision")
	}
	if _, err := json.Marshal(StoragePutParams{}); err == nil {
		t.Fatal("missing mutation parameters")
	}
	value := StorageGetResult{Found: true, Revision: &key, Value: json.RawMessage(`{"n":9007199254740992}`)}
	if _, err := json.Marshal(value); err == nil {
		t.Fatal("unsafe opaque integer accepted")
	}
	value.Value = json.RawMessage(`{"n":1e3}`)
	if _, err := json.Marshal(value); err != nil {
		t.Fatal(err)
	}
}
func TestHostRPCEncodedByteLimit(t *testing.T) {
	raw := []byte(`{"found":true,"value":"` + strings.Repeat("a", MaxHostRPCDTOBytes) + `","revision":"r"}`)
	if ValidateHostRPCDTO("StorageGetResult", raw) == nil {
		t.Fatal("oversize DTO accepted")
	}
	revision := "r"
	v := StorageGetResult{Found: true, Revision: &revision, Value: raw}
	if _, err := json.Marshal(v); err == nil {
		t.Fatal("oversize producer value accepted")
	}
	// UTF-8 bytes, not characters, determine admission.
	raw = []byte(`{"message":"` + strings.Repeat("😀", MaxHostRPCDTOBytes/4) + `","level":"info","grant_id":"g","context":{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1}}}`)
	if ValidateHostRPCDTO("LogParams", raw) == nil {
		t.Fatal("encoded multibyte bytes not counted")
	}
}
func TestHostRPCHeaderAndURLConstraints(t *testing.T) {
	for _, raw := range []string{`{"name":"X-Test","value":"one\r\ntwo"}`, `{"name":"Bad Name","value":""}`} {
		if ValidateHostRPCDTO("Header", []byte(raw)) == nil {
			t.Fatal("invalid header accepted")
		}
	}
	base := `{"grant_id":"g","context":{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1}},"method":"GET","url":`
	for _, url := range []string{"https://user:pass@example.com", "https://@example.com", "https:///example.com", "https://example.com/#fragment", "http://example.com", "https:/example.com", "https://example.com/path with space", "https://example.com/%ZZ", "https://example.com:99999/"} {
		encoded, _ := json.Marshal(url)
		if ValidateHostRPCDTO("EgressRequestParams", append(append([]byte(base), encoded...), '}')) == nil {
			t.Fatal("invalid URL accepted")
		}
	}
}
func TestHostRPCOpaqueKeysRemainExact(t *testing.T) {
	raw := []byte(`{"found":true,"revision":"r","value":{"a":1,"\\\\":2,"\\\"":3,"__proto__":4}}`)
	var v StorageGetResult
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(v.Value, []byte(`"\\\"":3`)) {
		t.Fatal("opaque key bytes changed")
	}
}
