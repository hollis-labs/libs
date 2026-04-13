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
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("method constant %q != %q", got, want)
		}
	}
}

func TestLoadResultRoundtrip(t *testing.T) {
	in := LoadResult{
		Dependencies:       []string{"other-plugin"},
		EventSubscriptions: []string{"message.sent"},
		CRUDResources:      []string{"bookmark"},
		Commands: []CommandRegistration{{
			Name:        "bk",
			Description: "bookmark a URL",
			Category:    "tools",
			Args: []CommandArg{
				{Name: "url", Required: true, Type: "string"},
			},
		}},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out LoadResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Commands) != 1 || out.Commands[0].Name != "bk" {
		t.Errorf("command roundtrip lost data: %+v", out.Commands)
	}
	if len(out.Commands[0].Args) != 1 || out.Commands[0].Args[0].Name != "url" {
		t.Errorf("command args roundtrip lost data: %+v", out.Commands[0].Args)
	}
}
