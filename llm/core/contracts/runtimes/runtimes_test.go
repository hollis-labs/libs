package runtimes

import (
	"encoding/json"
	"testing"
)

func TestListsAreValidAndDistinct(t *testing.T) {
	seenID := map[ID]bool{}
	for _, id := range IDs() {
		if !id.Valid() || seenID[id] {
			t.Errorf("IDs: %q invalid or repeated", id)
		}
		seenID[id] = true
	}
	seenMode := map[Mode]bool{}
	for _, m := range Modes() {
		if !m.Valid() || seenMode[m] {
			t.Errorf("Modes: %q invalid or repeated", m)
		}
		seenMode[m] = true
	}
	seenCap := map[Capability]bool{}
	for _, c := range Capabilities() {
		if !c.Valid() || seenCap[c] {
			t.Errorf("Capabilities: %q invalid or repeated", c)
		}
		seenCap[c] = true
	}
	if len(seenID) != 6 || len(seenMode) != 7 || len(seenCap) != 7 {
		t.Errorf("got %d ids, %d modes, %d capabilities; want 6, 7, 7", len(seenID), len(seenMode), len(seenCap))
	}
}

// The other enumerations this one replaced (D-73) have no spelling here: no
// alias, no legacy constant (D-22). Runtime aliases such as claude-code belong
// to the go-providers registry, not to ID.
func TestEnumsRejectOutsiders(t *testing.T) {
	for _, s := range []string{"", "Claude", "claude-code", "gemini", "agy", "pi-acp"} {
		if ID(s).Valid() {
			t.Errorf("ID(%q) is Valid", s)
		}
	}
	for _, s := range []string{"", "app-server", "serve-http", "subprocess", "exec", "cli", "api", "pty-debug",
		"claude-print", "claude-bare", "codex-app-server", "opencode-serve-http", "acp", "STREAMING-STDIO"} {
		if Mode(s).Valid() {
			t.Errorf("Mode(%q) is Valid", s)
		}
	}
	for _, s := range []string{"", "identity", "mcp", "posture", "resume_keeps_id"} {
		if Capability(s).Valid() {
			t.Errorf("Capability(%q) is Valid", s)
		}
	}
}

func TestModeACP(t *testing.T) {
	for _, m := range Modes() {
		want := m == ModeACPStdio || m == ModeACPTCP
		if m.ACP() != want {
			t.Errorf("%s.ACP() = %v, want %v", m, m.ACP(), want)
		}
	}
	if Mode("acp").ACP() {
		t.Error(`Mode("acp").ACP() is true; only the two ACP transports are ACP`)
	}
}

// The constant values are the wire spellings; they survive JSON unchanged.
func TestValuesRoundTripThroughJSON(t *testing.T) {
	type wrap struct {
		I ID         `json:"i"`
		M Mode       `json:"m"`
		C Capability `json:"c"`
	}
	for _, id := range IDs() {
		for _, m := range Modes() {
			for _, c := range Capabilities() {
				in := wrap{id, m, c}
				raw, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				var out wrap
				if err := json.Unmarshal(raw, &out); err != nil {
					t.Fatal(err)
				}
				if out != in {
					t.Fatalf("round trip changed %+v to %+v", in, out)
				}
			}
		}
	}
}

func TestListsAreFreshCopies(t *testing.T) {
	ids := IDs()
	ids[0] = "mutated"
	modes := Modes()
	modes[0] = "mutated"
	caps := Capabilities()
	caps[0] = "mutated"
	if IDs()[0] != Claude || Modes()[0] != ModeStreamingStdio || Capabilities()[0] != CapResume {
		t.Error("a caller's edit to a returned list leaked into the next call")
	}
}
