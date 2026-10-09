// Package cmdhook runs a command-kind hook as a real subprocess: the hook's
// input is written to stdin as JSON, and stdout and the exit code are decoded
// into a [hooks.Output]. It is the one I/O-performing helper in go-hooks and
// imports only the root package.
//
// Exit-code conventions follow Claude Code:
//
//   - 0: success. Empty stdout yields the zero Output; otherwise stdout is
//     decoded as JSON into hooks.Output and validated.
//   - 2: an intentional block, not a failure. stderr becomes the reason and
//     Run returns Output{Decision: deny, Reason: stderr} with a nil error.
//   - anything else: an execution failure. Run returns an error wrapping the
//     exit status and stderr.
//
// A [Runner] never reads Hook.OnError. Failures, timeouts and invalid output
// come back as errors and the caller applies the hook's declared mode. It
// never truncates AdditionalContext and does not special-case Hook.Async.
// There is no sandbox: the script runs with the host's authority.
package cmdhook
