package main

import (
	"fmt"

	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

func main() {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1200, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 8000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 500, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 300, Provenance: usageledger.ProvenanceMeasured}
	// ReasoningTokens is left untouched: the provider did not report it.

	fmt.Println(u.Validate() == nil)
	fmt.Println(u.TotalTokens(), u.TotalProvenance())

	row := usageledger.Row{Provider: "anthropic", Model: "example-model", Usage: u}
	fmt.Println(row.Provider, row.Model, row.Usage.TotalTokens(), row.Price == nil)
}
