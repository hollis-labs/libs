package streamhub_test

import (
	"fmt"

	streamhub "github.com/hollis-labs/go-streamhub"
)

func ExampleHello() {
	fmt.Println(streamhub.Hello())
	// Output: hello from streamhub
}
