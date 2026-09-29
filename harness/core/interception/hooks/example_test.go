package hooks_test

import (
	"fmt"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
)

func ExampleResolve() {
	project := []hooks.Hook{{Name: "guard", Event: hooks.EventPreToolUse, Kind: hooks.KindCommand,
		Command: "/opt/guard-lax", Timeout: time.Second, OnError: hooks.OnErrorOpen}}
	managed := []hooks.Hook{{Name: "guard", Event: hooks.EventPreToolUse, Kind: hooks.KindCommand,
		Command: "/opt/guard-strict", Timeout: 5 * time.Second, OnError: hooks.OnErrorClosed}}

	resolved, err := hooks.Resolve(managed, nil, project)
	if err != nil {
		panic(err)
	}
	for _, h := range resolved {
		fmt.Println(h.Name, h.Layer, h.Command, h.OnError, h.Validate() == nil)
	}
	// Output: guard managed /opt/guard-strict closed true
}

func ExampleHook_Validate() {
	err := hooks.Hook{Name: "guard", Event: hooks.EventStop, Kind: hooks.KindCommand, Command: "x"}.Validate()
	fmt.Println(err)
	// Output: hooks: invalid hook guard: Timeout: must be > 0; OnError: required (open or closed); there is no default
}

func ExampleMatchesTool() {
	fmt.Println(hooks.MatchesTool("mcp__memory__*", "mcp__memory__write"))
	fmt.Println(hooks.MatchesTool("Bash", "Edit"))
	// Output:
	// true
	// false
}

func ExampleTruncateContext() {
	fmt.Println(hooks.TruncateContext("héllo", 2))
	// Output: h
}
