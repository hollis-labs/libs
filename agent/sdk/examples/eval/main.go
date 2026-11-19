// This program accepts only a scenario file and runs synthetic adapters. It has
// no live model/tool/host configuration and makes no network or service calls.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hollis-labs/libs/agent/sdk/eval"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./examples/eval path/to/scenario.yaml")
		os.Exit(2)
	}
	file, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer file.Close()
	scenario, err := eval.Decode(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	result, err := eval.Run(context.Background(), scenario, eval.Options{})
	fmt.Printf("%s passed=%t turns=%d fake_tool_calls=%d findings=%d\n", result.ScenarioID, result.Passed, result.Metrics.Turns, result.Metrics.FakeToolCalls, len(result.Findings))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !result.Passed {
		os.Exit(1)
	}
}
