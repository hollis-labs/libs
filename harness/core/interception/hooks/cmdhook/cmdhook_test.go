package cmdhook_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
	"github.com/hollis-labs/go-hooks/cmdhook"
)

// script writes an executable shell script and returns a command hook for it.
func script(t *testing.T, body string, timeout time.Duration) hooks.Hook {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hook.sh")
	//nolint:gosec // G306: test scripts must be executable.
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return hooks.Hook{
		Name: "t", Event: hooks.EventPreToolUse, Kind: hooks.KindCommand,
		Command: p, Timeout: timeout, OnError: hooks.OnErrorClosed,
	}
}

func input() hooks.PreToolUseInput {
	return hooks.PreToolUseInput{
		CommonInput: hooks.CommonInput{Event: hooks.EventPreToolUse, SessionID: "s", Cwd: "/w"},
		ToolName:    "Bash",
		ToolInput:   map[string]any{"command": "ls"},
	}
}

func TestRunExitZeroValidJSON(t *testing.T) {
	h := script(t, `cat >/dev/null; printf '%s' '{"decision":"allow","additionalContext":"ctx","continue":true}'`, 5*time.Second)
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != hooks.DecisionAllow || out.AdditionalContext != "ctx" || out.Continue == nil || !*out.Continue {
		t.Errorf("out = %+v", out)
	}
}

func TestRunWritesInputAsJSONToStdin(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "stdin.json")
	h := script(t, `cat > "`+dump+`"`, 5*time.Second)
	if _, err := (cmdhook.Runner{}).Run(context.Background(), h, input()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dump) //nolint:gosec // G304: path is a test temp file.
	if err != nil {
		t.Fatal(err)
	}
	want := `{"hook_event_name":"PreToolUse","session_id":"s","cwd":"/w","tool_name":"Bash","tool_input":{"command":"ls"}}`
	if string(b) != want {
		t.Errorf("stdin = %s\nwant    %s", b, want)
	}
}

func TestRunExitZeroEmptyStdout(t *testing.T) {
	h := script(t, `cat >/dev/null`, 5*time.Second)
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(out, hooks.Output{}) {
		t.Errorf("out = %+v, want zero", out)
	}
}

func TestRunExitZeroWhitespaceStdout(t *testing.T) {
	h := script(t, `printf '  \n\n'`, 5*time.Second)
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err != nil || !reflect.DeepEqual(out, hooks.Output{}) {
		t.Errorf("out = %+v err = %v", out, err)
	}
}

func TestRunInvalidJSONIsAnErrorNotAPanic(t *testing.T) {
	h := script(t, `printf '%s' 'not json {'`, 5*time.Second)
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err == nil || !strings.Contains(err.Error(), "invalid output") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(out, hooks.Output{}) {
		t.Errorf("out = %+v on error", out)
	}
}

func TestRunUnknownDecisionIsAnError(t *testing.T) {
	h := script(t, `printf '%s' '{"decision":"block"}'`, 5*time.Second)
	_, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err == nil || !strings.Contains(err.Error(), "invalid decision") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunExitTwoIsADenyNotAnError(t *testing.T) {
	h := script(t, `cat >/dev/null; printf '%s' '{"decision":"allow"}'; echo "refusing rm -rf" >&2; exit 2`, 5*time.Second)
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err != nil {
		t.Fatalf("exit 2 is a block, got err %v", err)
	}
	if out.Decision != hooks.DecisionDeny || out.Reason != "refusing rm -rf" {
		t.Errorf("out = %+v (stdout must be ignored on exit 2)", out)
	}
}

func TestRunExitTwoWithOnErrorOpenStillDenies(t *testing.T) {
	h := script(t, `echo nope >&2; exit 2`, 5*time.Second)
	h.OnError = hooks.OnErrorOpen
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err != nil || out.Decision != hooks.DecisionDeny {
		t.Errorf("OnError must not apply to exit 2: out=%+v err=%v", out, err)
	}
}

func TestRunOtherNonzeroExitIsAnErrorNotADecision(t *testing.T) {
	for _, code := range []string{"1", "3", "17", "126"} {
		h := script(t, `echo "boom" >&2; exit `+code, 5*time.Second)
		out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
		if err == nil {
			t.Fatalf("exit %s: no error", code)
		}
		if !reflect.DeepEqual(out, hooks.Output{}) {
			t.Errorf("exit %s: got a decision %+v", code, out)
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Errorf("exit %s: error does not wrap *exec.ExitError: %v", code, err)
		}
		if !strings.Contains(err.Error(), "exit status "+code) || !strings.Contains(err.Error(), "boom") {
			t.Errorf("exit %s: error %q lacks status or stderr", code, err)
		}
	}
}

func TestRunEnforcesTimeoutWithoutCallerDeadline(t *testing.T) {
	h := script(t, `sleep 30`, 200*time.Millisecond)
	start := time.Now()
	_, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s; the script was not killed", d)
	}
}

func TestRunTimeoutKillsPipeHoldingChild(t *testing.T) {
	// The shell's background child inherits stdout; without WaitDelay the
	// wait would block until it exits.
	h := script(t, `sleep 30 & wait`, 200*time.Millisecond)
	start := time.Now()
	_, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestRunTimeoutBeatsExitTwo(t *testing.T) {
	h := script(t, `trap 'exit 2' TERM; sleep 30 & wait`, 200*time.Millisecond)
	_, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a timed-out hook must not read as a deliberate block: %v", err)
	}
}

func TestRunCallerCancel(t *testing.T) {
	h := script(t, `sleep 30`, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := cmdhook.Runner{}.Run(ctx, h, input())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("cancel reported as a timeout")
	}
}

func TestRunRejectsBadHooks(t *testing.T) {
	good := script(t, `:`, time.Second)
	noTimeout := good
	noTimeout.Timeout = 0
	mcp := good
	mcp.Kind = hooks.KindMCPTool
	noCmd := good
	noCmd.Command = ""
	for name, h := range map[string]hooks.Hook{"no timeout": noTimeout, "mcp kind": mcp, "no command": noCmd} {
		if _, err := (cmdhook.Runner{}).Run(context.Background(), h, input()); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRunMissingBinary(t *testing.T) {
	h := script(t, `:`, time.Second)
	h.Command = filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := (cmdhook.Runner{}).Run(context.Background(), h, input()); err == nil {
		t.Fatal("missing binary is not a failure")
	}
}

func TestRunUnencodableInput(t *testing.T) {
	h := script(t, `:`, time.Second)
	if _, err := (cmdhook.Runner{}).Run(context.Background(), h, make(chan int)); err == nil {
		t.Fatal("unencodable input accepted")
	}
}

func TestRunnerStartOverride(t *testing.T) {
	var called bool
	r := cmdhook.Runner{Start: func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s' '{"systemMessage":"overridden"}'`)
	}}
	h := script(t, `exit 9`, time.Second)
	out, err := r.Run(context.Background(), h, input())
	if err != nil || !called || out.SystemMessage != "overridden" {
		t.Errorf("out=%+v err=%v called=%v", out, err, called)
	}
}

func TestRunnerNeverTruncatesAdditionalContext(t *testing.T) {
	h := script(t, `printf '%s' '{"additionalContext":"0123456789"}'`, 5*time.Second)
	h.AdditionalContextLimit = 3
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err != nil || out.AdditionalContext != "0123456789" {
		t.Errorf("out=%+v err=%v", out, err)
	}
}

func TestRunnerIgnoresAsync(t *testing.T) {
	h := script(t, `printf '%s' '{"decision":"deny"}'`, 5*time.Second)
	h.Async = true
	out, err := cmdhook.Runner{}.Run(context.Background(), h, input())
	if err != nil || out.Decision != hooks.DecisionDeny {
		t.Errorf("out=%+v err=%v", out, err)
	}
}
