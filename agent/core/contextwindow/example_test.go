package contextwindow_test

import (
	"fmt"

	contextwindow "github.com/hollis-labs/go-context-window"
)

func ExampleHello() {
	fmt.Println(contextwindow.Hello())
	// Output: hello from contextwindow
}
