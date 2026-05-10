// Package main demonstrates building a ChatRequest with a system prompt,
// a single user message, and an optional tool definition. Producing a
// ChatRequest is the entry point for any provider call in the Hollis
// Labs LLM toolchain.
//
// Run:
//
//	go run ./examples/chatrequest
package main

import (
	"encoding/json"
	"fmt"

	llmtypes "github.com/hollis-labs/go-llm-types"
)

func main() {
	strict := true

	req := llmtypes.ChatRequest{
		Model:        "claude-3-5-sonnet",
		SystemPrompt: "You are a concise assistant.",
		Messages: []llmtypes.ChatMessage{
			{Role: "user", Content: "What is the capital of France?"},
		},
		Tools: []llmtypes.ToolDefinition{
			{
				Name:        "lookup_capital",
				Description: "Look up the capital city for a country.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"country": map[string]any{"type": "string"},
					},
					"required": []string{"country"},
				},
				Strict: &strict,
			},
		},
		MaxTokens: 1024,
	}

	fmt.Println("Effective system prompt:")
	fmt.Println(req.EffectiveSystemPrompt())
	fmt.Println()

	out, _ := json.MarshalIndent(req.Tools, "", "  ")
	fmt.Println("Tool definitions:")
	fmt.Println(string(out))
}
