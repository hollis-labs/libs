package panics_test

import (
	"fmt"
	"os"
	"strings"

	"github.com/hollis-labs/libs/core/panics"
)

func Example() {
	g := panics.New(panics.Config{
		Reporter: panics.StderrReporter(os.Stderr),
		Redact:   func(s string) string { return strings.ReplaceAll(s, "hunter2", "[redacted]") },
	})
	err := g.Call("example boundary", func() error { panic("password is hunter2") })
	_, isPanic := panics.Ref(err)
	fmt.Println(isPanic, strings.HasPrefix(err.Error(), "internal error (ref pnc_"))
	// Output: true true
}
