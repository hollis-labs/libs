package paths_test

import (
	"fmt"

	paths "github.com/hollis-labs/libs/util/apppaths"
)

// Every directory go-apppaths creates or owns is forced owner-only, including
// ones that already existed at a looser mode.
func ExampleDirMode() {
	fmt.Printf("dirs %#o, files %#o\n", paths.DirMode, paths.FileMode)
	// Output: dirs 0700, files 0600
}
