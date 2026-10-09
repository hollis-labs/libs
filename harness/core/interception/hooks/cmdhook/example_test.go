package cmdhook_test

import (
	"context"
	"fmt"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
	"github.com/hollis-labs/go-hooks/cmdhook"
)

func ExampleRunner_Run() {
	h := hooks.Hook{
		Name: "greet", Event: hooks.EventSessionStart, Kind: hooks.KindCommand,
		Command:     "/bin/sh",
		CommandArgs: []string{"-c", `cat >/dev/null; printf '{"additionalContext":"hello"}'`},
		Timeout:     5 * time.Second,
		OnError:     hooks.OnErrorOpen,
	}
	in := hooks.SessionStartInput{
		CommonInput: hooks.CommonInput{Event: hooks.EventSessionStart, SessionID: "s1", Cwd: "/work"},
		Source:      "startup",
	}
	out, err := cmdhook.Runner{}.Run(context.Background(), h, in)
	fmt.Println(out.AdditionalContext, err)
	// Output: hello <nil>
}

func ExampleRunner_Run_block() {
	h := hooks.Hook{
		Name: "guard", Event: hooks.EventPreToolUse, Kind: hooks.KindCommand,
		Command:     "/bin/sh",
		CommandArgs: []string{"-c", `cat >/dev/null; echo "refusing rm -rf" >&2; exit 2`},
		Timeout:     5 * time.Second,
		OnError:     hooks.OnErrorClosed,
	}
	out, err := cmdhook.Runner{}.Run(context.Background(), h, hooks.PreToolUseInput{
		CommonInput: hooks.CommonInput{Event: hooks.EventPreToolUse},
		ToolName:    "Bash",
	})
	fmt.Println(out.Decision, out.Reason, err)
	// Output: deny refusing rm -rf <nil>
}
