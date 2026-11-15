// This example runs only in-memory synthetic hosts/models/tools. It starts no
// service, needs no credentials and grants no filesystem or network access.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
)

func main() {
	host := fakehost.New()
	operative, err := sdk.New(fakehost.Definition(), host.Ports())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	result, err := operative.Run(context.Background(), host.Input())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s: %s\nturns=%d effects=%d input_tokens=%d output_tokens=%d events=%d\n", result.Status, result.Text, result.Turns, host.Effects, result.Usage.InputTokens, result.Usage.OutputTokens, len(host.Events))
}
