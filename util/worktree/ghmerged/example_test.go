package ghmerged_test

import (
	"fmt"

	"github.com/hollis-labs/libs/util/worktree/ghmerged"
)

// A Source is configured, not called, here: Merged shells out to gh in the
// repository directory and is used as the source of worktree.MergedPR.
func ExampleNew() {
	src := ghmerged.New(ghmerged.WithLimit(100))
	fmt.Printf("%T\n", src)
	// Output: *ghmerged.Source
}
