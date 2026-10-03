// Command ownerperms is a runnable example of owner-only permissions: it
// points HOME at a throwaway directory, leaves an app root at a loose 0755 the
// way a v0.1.x install would, resolves, and prints the modes it ends up with.
//
// Usage:
//
//	go run ./examples/ownerperms
package main

import (
	"fmt"
	"os"
	"path/filepath"

	paths "github.com/hollis-labs/libs/util/apppaths"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ownerperms:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.MkdirTemp("", "ownerperms-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()
	if err := os.Setenv("HOME", home); err != nil {
		return err
	}
	for _, v := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		if err := os.Unsetenv(v); err != nil {
			return err
		}
	}

	// A root left world-listable by an older release.
	loose := filepath.Join(home, ".local", "share", "demo")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		return err
	}
	fmt.Printf("before: %-8s %#o\n", "data", mode(loose))

	layout, err := paths.Resolve("demo")
	if err != nil {
		return err
	}
	fmt.Printf("after:  %-8s %#o (want %#o)\n", "data", mode(layout.DataDir()), paths.DirMode)
	fmt.Printf("after:  %-8s %#o\n", "state", mode(layout.StateDir()))
	return nil
}

func mode(p string) os.FileMode {
	info, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return info.Mode().Perm()
}
