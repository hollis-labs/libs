package redact_test

import (
	"fmt"

	"github.com/hollis-labs/libs/security/redact"
)

func Example() {
	// Patterns find secrets you do not know about; a Scope protects the ones you do.
	scope := redact.NewScope()
	scope.Add("DB_PASS", "correct-horse-battery")
	r, _ := redact.New(redact.WithScope(scope))

	fmt.Println(r.Text("Authorization: Bearer abc123.def456-ghi789 and db pass correct-horse-battery"))
	fmt.Println(r.Text(`{"user":"bob","password":"hunter2hunter2","max_tokens":1000}`))
	// Output:
	// Authorization: Bearer [REDACTED] and db pass [REDACTED]
	// {"user":"bob","password":"[REDACTED]","max_tokens":1000}
}
