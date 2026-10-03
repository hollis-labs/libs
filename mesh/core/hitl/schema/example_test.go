package schema_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/mesh/hitl/schema"
)

func ExampleNewValidator() {
	v, err := schema.NewValidator("HITLAwaitCommandV1")
	if err != nil {
		panic(err)
	}
	ok := `{"contract_version":"1.0","item_id":"item_1","caller":{"application_id":"app"},"wait_ms":30000}`
	tooLong := `{"contract_version":"1.0","item_id":"item_1","caller":{"application_id":"app"},"wait_ms":50001}`
	fmt.Println(v.Validate([]byte(ok)) == nil)
	fmt.Println(v.Validate([]byte(tooLong)) == nil)
	// Output:
	// true
	// false
}

func ExampleDefs() {
	fmt.Println(len(schema.Defs()) > 30)
	// Output: true
}
