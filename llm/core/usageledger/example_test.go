package usageledger_test

import (
	"fmt"

	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

func ExampleNewUsage() {
	u := usageledger.NewUsage()
	fmt.Println(u.ReasoningTokens.Provenance, u.ReasoningTokens.Tokens)
	fmt.Println(u.Validate() == nil, u.TotalTokens(), u.TotalProvenance())
	fmt.Println(usageledger.Usage{}.Validate() != nil)
	// Output:
	// unknown 0
	// true 0 unknown
	// true
}

func ExampleUsage_TotalTokens() {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1200, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 8000, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 300, Provenance: usageledger.ProvenanceMeasured}
	u.SetDim("tool_input", usageledger.Component{Tokens: 50, Provenance: usageledger.ProvenanceEstimated})
	fmt.Println(u.TotalTokens())
	// Output: 9550
}

func ExampleUsage_TotalProvenance() {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 10, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 0, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 0, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 5, Provenance: usageledger.ProvenanceMeasured}
	fmt.Println(u.TotalProvenance()) // reasoning was never reported
	u.ReasoningTokens = usageledger.Component{Tokens: 2, Provenance: usageledger.ProvenanceEstimated}
	fmt.Println(u.TotalProvenance())
	// Output:
	// unknown
	// estimated
}

func ExampleUsage_Validate() {
	u := usageledger.NewUsage()
	u.SetDim("output_tokens", usageledger.Component{Tokens: 1, Provenance: usageledger.ProvenanceMeasured})
	fmt.Println(u.Validate())
	// Output: usageledger: dims["output_tokens"]: key collides with core field "output_tokens"
}

func ExampleUsage_SetDim() {
	var u usageledger.Usage // Dims is nil; SetDim allocates it.
	u.SetDim("tool_input", usageledger.Component{Tokens: 7, Provenance: usageledger.ProvenanceMeasured})
	fmt.Println(len(u.Dims), u.TotalTokens())
	// Output: 1 7
}

func ExampleProvenance_Valid() {
	fmt.Println(usageledger.ProvenanceEstimated.Valid(), usageledger.Provenance("").Valid())
	// Output: true false
}

func ExampleRow() {
	row := usageledger.Row{Provider: "anthropic", Model: "example-model", Usage: usageledger.NewUsage()}
	fmt.Println(row.Provider, row.Model, row.Usage.TotalTokens(), row.Price == nil)
	row.Price = &usageledger.PriceSnapshot{InputPerMillion: 3, OutputPerMillion: 15}
	fmt.Println(row.Price.OutputPerMillion)
	// Output:
	// anthropic example-model 0 true
	// 15
}

// Example_quickstart mirrors examples/hello/main.go and the README usage fence.
func Example_quickstart() {
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
	// Output:
	// true
	// 10000 unknown
	// anthropic example-model 10000 true
}
