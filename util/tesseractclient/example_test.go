package tesseract_test

import (
	"fmt"

	tesseract "github.com/hollis-labs/go-tesseract-client"
)

func ExampleHello() {
	fmt.Println(tesseract.Hello())
	// Output: hello from tesseract
}
