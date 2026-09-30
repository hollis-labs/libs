package svcerr_test

import (
	"fmt"

	svcerr "github.com/hollis-labs/go-svcerr"
)

func ExampleHello() {
	fmt.Println(svcerr.Hello())
	// Output: hello from svcerr
}
