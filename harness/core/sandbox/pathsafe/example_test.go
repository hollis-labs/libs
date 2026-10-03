package pathsafe_test

import (
	"errors"
	"fmt"
	"os"

	"github.com/hollis-labs/substrate/harness/sandbox/pathsafe"
)

func ExampleResolveUnder() {
	root, err := os.MkdirTemp("", "pathsafe-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(root) }()

	// A path that stays inside the root resolves to an absolute path, even if
	// the leaf does not exist yet.
	if _, rerr := pathsafe.ResolveUnder(root, "notes/todo.txt"); rerr == nil {
		fmt.Println("inside: ok")
	}

	// A traversal attempt yields an *EscapeError.
	_, err = pathsafe.ResolveUnder(root, "../../etc/passwd")
	var esc *pathsafe.EscapeError
	fmt.Println("escape:", errors.As(err, &esc))
	// Output:
	// inside: ok
	// escape: true
}
