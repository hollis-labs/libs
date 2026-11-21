package agentdir

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTetherIdentity(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"", true}, {"auto", true}, {"msg://agent/test/agent-1", true}, {"msg://user/test/person", false}, {"msg://agent/test", false}, {"msg://agent/test/agent-1?x=y", false}, {" msg://agent/test/agent-1", false}} {
		t.Run(tc.value, func(t *testing.T) {
			dir := t.TempDir()
			raw, err := os.ReadFile("examples/agents/embedded/agent.md")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "agent.md"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			profile := "kind: embedded\nlifetime: application\napplication_id: test\nidentity:\n  tether: " + strconv.Quote(tc.value) + "\n"
			if err = os.WriteFile(filepath.Join(dir, "host.yaml"), []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			snap, err := Load(dir)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if snap.Profile.Identity == nil || snap.Profile.Identity.Tether != tc.value {
					t.Fatalf("identity: %+v", snap.Profile.Identity)
				}
			} else if !errors.Is(err, ErrProfile) || !strings.Contains(err.Error(), "identity.tether") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
