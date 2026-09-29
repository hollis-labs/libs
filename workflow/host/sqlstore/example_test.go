package sqlstore_test

import (
	"fmt"

	"github.com/hollis-labs/go-workflow-host/sqlstore"
)

func ExampleHello() {
	fmt.Println(sqlstore.Hello())
	// Output: hello from sqlstore
}
