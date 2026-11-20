package agentdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDailyTokenBudgetProfile(t *testing.T) {
	for _, limit := range []int{0, 1, 1000000, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			dir := t.TempDir()
			agent, err := os.ReadFile(filepath.Join("examples", "agents", "embedded", "agent.md"))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "agent.md"), agent, 0600); err != nil {
				t.Fatal(err)
			}
			profile := fmt.Sprintf("kind: embedded\nlifetime: application\napplication_id: budget-test\nruntime:\n  daily_token_budget: %d\n", limit)
			if err = os.WriteFile(filepath.Join(dir, "host.yaml"), []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			snap, err := Load(dir)
			valid := limit >= 0
			if valid {
				if err != nil || snap.Profile.Runtime.DailyTokenBudget != limit {
					t.Fatalf("snapshot=%+v err=%v", snap.Profile, err)
				}
			} else if !errors.Is(err, ErrProfile) || !strings.Contains(err.Error(), "runtime.daily_token_budget") {
				t.Fatalf("expected named validation error, got %v", err)
			}
		})
	}
}
