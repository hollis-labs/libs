// Package main demonstrates composing a system prompt from a base prompt
// plus pre-assembled context-window regions ("slots"). SlotBlocks are
// useful when different upstream stages own different sections of the
// system prompt — for example a project description, a coding-style
// guide, and a per-task instruction set.
//
// Run:
//
//	go run ./examples/slots
package main

import (
	"fmt"

	llmtypes "github.com/hollis-labs/go-llm-types"
)

func main() {
	req := llmtypes.ChatRequest{
		Model:        "claude-3-5-sonnet",
		SystemPrompt: "You are a senior Go engineer.",
		SlotBlocks: []llmtypes.SlotBlock{
			{
				Name:    "project-context",
				Content: "Project: a transport-agnostic LLM types module.",
			},
			{
				// Empty slots are silently dropped by EffectiveSystemPrompt.
				Name:    "user-preferences",
				Content: "",
			},
			{
				Name:     "task-instructions",
				Content:  "Prefer small, well-documented public APIs.",
				CacheKey: "task-instr-v1",
				Changed:  false,
			},
		},
	}

	fmt.Println(req.EffectiveSystemPrompt())
}
