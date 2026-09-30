package pathsafe_test

import (
	"fmt"

	"github.com/hollis-labs/go-safefs/pathsafe"
)

func ExampleHello() {
	fmt.Println(pathsafe.Hello())
	// Output: hello from pathsafe
}
