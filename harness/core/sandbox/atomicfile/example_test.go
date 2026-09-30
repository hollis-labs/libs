package atomicfile_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hollis-labs/go-safefs/atomicfile"
)

func ExampleWriteFile() {
	dir, err := os.MkdirTemp("", "atomicfile-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "config.json")
	if err := atomicfile.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
		fmt.Println(err)
		return
	}
	got, _ := os.ReadFile(path)
	fmt.Println(string(got))
	// Output: {"ok":true}
}
