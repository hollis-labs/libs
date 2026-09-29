package permission_test

import (
	"fmt"

	permission "github.com/hollis-labs/go-permission"
)

func ExampleHello() {
	fmt.Println(permission.Hello())
	// Output: hello from permission
}
