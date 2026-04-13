package subprocess

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRPCErrorImplementsError(t *testing.T) {
	var err error = &RPCError{Code: ErrCodeNotFound, Message: "missing"}
	var target *RPCError
	if !errors.As(err, &target) {
		t.Fatalf("errors.As failed")
	}
	if target.Code != ErrCodeNotFound {
		t.Errorf("code = %d, want %d", target.Code, ErrCodeNotFound)
	}
}

func TestRPCRequestRoundtrip(t *testing.T) {
	in := RPCRequest{
		JSONRPC: "2.0",
		ID:      42,
		Method:  MethodCommandExecute,
		Params: CommandExecParams{
			Name:      "bookmark",
			SessionID: "sess-1",
			Args:      "http://x",
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out RPCRequest
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != in.ID {
		t.Errorf("ID = %d, want %d", out.ID, in.ID)
	}
	if out.Method != in.Method {
		t.Errorf("Method = %q, want %q", out.Method, in.Method)
	}
}

func TestProtocolVersionLockedAt1(t *testing.T) {
	// Bumping this constant is a breaking change that must be coordinated
	// across host + all plugins. Fail loudly if someone changes it
	// inadvertently.
	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d; bump requires cross-repo coordination", ProtocolVersion)
	}
}

func TestMethodConstants(t *testing.T) {
	// These strings are part of the wire protocol — never rename.
	cases := map[string]string{
		MethodInit:           "plugin/init",
		MethodLoad:           "plugin/load",
		MethodUnload:         "plugin/unload",
		MethodHealth:         "plugin/health",
		MethodCommandExecute: "command/execute",
		MethodEventHandle:    "event/handle",
		MethodCRUDCreate:     "crud/create",
		MethodCRUDRead:       "crud/read",
		MethodCRUDUpdate:     "crud/update",
		MethodCRUDDelete:     "crud/delete",
		MethodCRUDList:       "crud/list",
		MethodListTools:      "mcp/list_tools",
		MethodMCPCallTool:    "mcp/call_tool",
		MethodHTTPHandle:     "http/handle",
		MethodMigrate:        "plugin/migrate",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("method constant %q != %q", got, want)
		}
	}
}

func TestLoadResultRoundtrip(t *testing.T) {
	in := LoadResult{
		SkippedRegistrations: []SkippedRegistration{
			{Kind: "command", ID: "bk", Reason: "api_key not set"},
			{Kind: "mcp_server", ID: "tools", Reason: "feature flag off"},
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out LoadResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.SkippedRegistrations) != 2 {
		t.Fatalf("SkippedRegistrations = %d, want 2", len(out.SkippedRegistrations))
	}
	if out.SkippedRegistrations[0].Kind != "command" || out.SkippedRegistrations[0].ID != "bk" {
		t.Errorf("skipped[0] = %+v", out.SkippedRegistrations[0])
	}
	if out.SkippedRegistrations[1].Reason != "feature flag off" {
		t.Errorf("skipped[1].Reason = %q", out.SkippedRegistrations[1].Reason)
	}
}

func TestLoadResultEmptyRoundtrip(t *testing.T) {
	// Empty LoadResult — the common case — must round-trip cleanly
	// and omit the skipped_registrations field.
	data, err := json.Marshal(LoadResult{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("empty LoadResult marshaled to %q, want %q", string(data), "{}")
	}
}
