// Command hello resolves a hook, validates it and runs it as a subprocess.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
	"github.com/hollis-labs/go-hooks/cmdhook"
)

func main() {
	guard := hooks.Hook{
		Name:        "guard-write-scope",
		Event:       hooks.EventPreToolUse,
		Kind:        hooks.KindCommand,
		Matcher:     "Write",
		Command:     "/bin/sh",
		CommandArgs: []string{"-c", `cat >/dev/null; echo "writes outside the workspace are refused" >&2; exit 2`},
		Timeout:     5 * time.Second,
		OnError:     hooks.OnErrorClosed, // required: there is no default
	}

	// Managed beats user beats project, by hook name.
	resolved, err := hooks.Resolve([]hooks.Hook{guard}, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	h := resolved[0]
	if verr := h.Validate(); verr != nil {
		log.Fatal(verr)
	}

	in := hooks.PreToolUseInput{
		CommonInput: hooks.CommonInput{Event: hooks.EventPreToolUse, SessionID: "s1", Cwd: "/work"},
		ToolName:    "Write",
		ToolInput:   map[string]any{"file_path": "/etc/passwd"},
	}
	if !hooks.MatchesTool(h.Matcher, in.ToolName) {
		return
	}
	out, err := cmdhook.Runner{}.Run(context.Background(), h, in)
	if err != nil {
		log.Fatal(err) // the caller applies h.OnError here
	}
	fmt.Println(out.Decision, "-", out.Reason)
}
