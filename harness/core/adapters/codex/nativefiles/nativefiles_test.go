package nativefiles

import (
	"encoding/json"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"strings"
	"testing"
)

func TestConfig(t *testing.T) {
	out, err := Config(ConfigInput{ApprovalPolicy: "never", SandboxMode: "workspace-write", WritableRoots: []string{"/fixture/project"}, Servers: []contract.Server{{Name: "http", HTTPURL: "http://example.invalid"}, {Name: "stdio", Command: "fixture", Env: []contract.Variable{{Name: "A", Value: "yes"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"approval_policy = \"never\"\nsandbox_mode = \"workspace-write\"\n", "[sandbox_workspace_write]", "[mcp_servers.http]", "[mcp_servers.stdio.env]", "\"A\" = \"yes\""} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	out, err = Config(ConfigInput{})
	if err != nil || len(out) != 0 {
		t.Fatalf("absent policy: %s %v", out, err)
	}
	for _, in := range []ConfigInput{
		{ApprovalPolicy: "untrusted"}, {SandboxMode: "invented"},
		{ApprovalPolicy: "never", Slots: []contract.Slot{{Key: "approval_policy", Value: "on-request"}}},
		{Servers: []contract.Server{{Name: "x", Command: "a"}, {Name: "x", Command: "b"}}},
	} {
		if _, err := Config(in); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := Config(ConfigInput{Slots: []contract.Slot{{Key: "bad", Value: func() {}}}}); err == nil {
		t.Fatal("invalid TOML value accepted")
	}
}

func TestConfigExactEncodingAndInvalidNativeValues(t *testing.T) {
	out, err := Config(ConfigInput{ApprovalPolicy: "never", SandboxMode: "workspace-write", Servers: []contract.Server{{Name: "loopback", HTTPURL: "http://example.invalid"}, {Name: "mux", Command: "fixture", Args: []string{"--stdio"}, Env: []contract.Variable{{Name: "A", Value: "yes"}}}}})
	want := "approval_policy = \"never\"\nsandbox_mode = \"workspace-write\"\n\n[mcp_servers.loopback]\nurl = \"http://example.invalid\"\n\n[mcp_servers.mux]\ncommand = \"fixture\"\nargs = [\"--stdio\"]\n\n[mcp_servers.mux.env]\n\"A\" = \"yes\"\n"
	if err != nil || string(out) != want {
		t.Fatalf("encoding mismatch:\n%s\n%v", out, err)
	}
	for _, slot := range []contract.Slot{
		{Key: "approval_policy", Value: []string{"never"}},
		{Key: "approval_policy", Value: "invented"},
		{Key: "sandbox_mode", Value: false},
		{Key: "n", Value: nil},
		{Key: "n", Value: uint64(18446744073709551615)},
	} {
		if _, err := Config(ConfigInput{Slots: []contract.Slot{slot}}); err == nil {
			t.Fatalf("invalid native value accepted for %s", slot.Key)
		}
	}
}

func TestInstalledEncodingPreservesArchivedDocument(t *testing.T) {
	in := ConfigInput{Mode: "install", Encoding: InstalledEncoding, ApprovalPolicy: "on-request", Servers: []contract.Server{{Name: "fixture", Command: "fixture-server", Args: []string{"one", "two"}}}}
	got, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "approval_policy = 'on-request'\n\n[mcp_servers]\n[mcp_servers.fixture]\nargs = ['one', 'two']\ncommand = 'fixture-server'\n"
	if string(got) != want {
		t.Fatalf("%q", got)
	}
}

func TestInstalledEncodingScalarAndArrayConventions(t *testing.T) {
	in := ConfigInput{Encoding: InstalledEncoding, Slots: []contract.Slot{{Key: "float", Value: json.Number("1e0")}, {Key: "escaped", Value: "can't\n<>&\x7f"}, {Key: "items", Value: []any{map[string]any{"name": "one"}, map[string]any{"name": "two"}}}}}
	got, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "escaped = \"can't\\n<>&\\u007F\"\nfloat = 1.0\n\n[[items]]\nname = 'one'\n\n[[items]]\nname = 'two'\n"
	if string(got) != want {
		t.Fatalf("%q", got)
	}
}
