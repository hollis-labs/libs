package contextwindow_test

import (
	"context"
	"fmt"
	"strings"

	contextwindow "github.com/hollis-labs/go-context-window"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

func ExampleNewContextWindow() {
	cw := contextwindow.NewContextWindow(200_000, contextwindow.DefaultEstimator{})
	cw.SetContent(contextwindow.SlotSystem, "You are a careful assistant.")
	cw.SetContent(contextwindow.SlotConversation, "user: hello")

	// Blocks come back in SlotOrder; empty slots are skipped.
	for _, b := range cw.Assemble() {
		fmt.Printf("%s changed=%v\n", b.SlotName, b.Changed)
	}
	// Output:
	// system changed=true
	// conversation changed=true
}

func ExampleCompactionPipeline_Run() {
	cw := contextwindow.NewContextWindow(1000, contextwindow.DefaultEstimator{})
	cw.SetContent(contextwindow.SlotContext, strings.Repeat("x", 1600)) // ~400 tokens of enrichment

	// Run only acts when the conversation slot is over budget, and it
	// re-derives that slot from ConversationMessages between stages.
	convo := strings.Repeat("y", 2000) // ~500 tokens
	cw.SetContent(contextwindow.SlotConversation, convo)
	msgs := []llmtypes.ChatMessage{{Role: "user", Content: convo}}

	p := &contextwindow.CompactionPipeline{
		Window:               cw,
		Estimator:            contextwindow.DefaultEstimator{},
		Mode:                 contextwindow.CompactionModeGeneral,
		ConversationMessages: msgs,
	}

	res, err := p.Run(context.Background())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	if res == nil {
		fmt.Println("no compaction needed")
		return
	}
	// Dropping the context-slot enrichment frees enough budget, so later
	// stages never run.
	fmt.Println(res.StagesApplied)
	// Output: [drop_enrichment]
}
