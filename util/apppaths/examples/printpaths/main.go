// Command printpaths is a runnable example of the paths package: it resolves
// the layout for a demo app and prints every entry — the same data an
// `<app> path` introspection subcommand would show.
//
// It uses WithoutMaterialize so the example has no filesystem side effects.
//
// Usage:
//
//	go run ./examples/printpaths
package main

import (
	"fmt"
	"os"

	paths "github.com/hollis-labs/libs/util/apppaths"
)

func main() {
	layout, err := paths.Resolve("demo", paths.WithoutMaterialize())
	if err != nil {
		fmt.Fprintln(os.Stderr, "printpaths: resolve:", err)
		os.Exit(1)
	}
	for _, e := range layout.Describe() {
		fmt.Printf("%-14s %s\n", e.Label, e.Value)
	}
}
