package chattest_test

import (
	"testing"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
)

func TestScriptedRunnerConformance(t *testing.T) {
	for name, opts := range map[string][]chattest.ScriptedOption{
		"deltas": nil,
		"whole":  {chattest.Whole()},
	} {
		t.Run(name, func(t *testing.T) {
			for _, text := range []string{"hello there", "tool", "reasoning", "error", "abort", "length"} {
				t.Run(text, func(t *testing.T) {
					chattest.RunnerConformance(t, chattest.RunnerHarness{
						New:  func(*testing.T) chat.Runner { return chattest.NewScripted(opts...) },
						Text: text, LongText: "hold", ApprovalText: "approval",
					})
				})
			}
		})
	}
}

func chatTurn(text string) chat.Turn { return chat.Turn{Text: text} }
