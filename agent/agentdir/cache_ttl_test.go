package agentdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheTTLProfile(t *testing.T) {
	for _, ttl := range []string{"", "5m", "1h", "60m", "0", "1H", " 1h", "2h"} {
		t.Run("ttl="+ttl, func(t *testing.T) {
			dir := t.TempDir()
			agent, err := os.ReadFile(filepath.Join("examples", "agents", "embedded", "agent.md"))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "agent.md"), agent, 0600); err != nil {
				t.Fatal(err)
			}
			profile := "kind: embedded\nlifetime: application\napplication_id: cache-test\nruntime:\n  cache_ttl: '" + ttl + "'\n"
			if err = os.WriteFile(filepath.Join(dir, "host.yaml"), []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			snap, err := Load(dir)
			valid := ttl == "" || ttl == "5m" || ttl == "1h"
			if valid {
				if err != nil || snap.Profile.Runtime.CacheTTL != ttl {
					t.Fatalf("snapshot=%+v err=%v", snap.Profile, err)
				}
			} else if !errors.Is(err, ErrProfile) || !strings.Contains(err.Error(), "runtime.cache_ttl") {
				t.Fatalf("expected named validation error, got %v", err)
			}
		})
	}
}
