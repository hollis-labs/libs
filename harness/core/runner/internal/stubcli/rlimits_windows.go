//go:build windows

package main

import (
	"fmt"
	"os"
)

func showResourceLimits() {
	fmt.Fprintln(os.Stderr, "resource limits are unsupported on Windows")
	os.Exit(2)
}
