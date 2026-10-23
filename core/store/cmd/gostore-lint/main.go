// Command gostore-lint checks Go source for SQL that is not portable between
// SQLite and Postgres. Usage: gostore-lint [-skip-tests] [dir ...]. It exits 1
// when it finds anything.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hollis-labs/libs/core/store/lint"
)

func main() {
	skip := flag.Bool("skip-tests", false, "leave *_test.go files out")
	flag.Parse()
	dirs := flag.Args()
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	bad := false
	for _, d := range dirs {
		fs, err := lint.Dir(d, lint.Options{SkipTests: *skip})
		if err != nil {
			fmt.Fprintln(os.Stderr, "gostore-lint:", err)
			os.Exit(2)
		}
		for _, f := range fs {
			fmt.Println(f)
			bad = true
		}
	}
	if bad {
		os.Exit(1)
	}
}
