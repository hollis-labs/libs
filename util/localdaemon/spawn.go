package localdaemon

import "os"

// Detach selects how a spawned daemon is separated from its parent.
type Detach int

const (
	// Session starts the child in a new session (setsid): no controlling
	// terminal, its own process group. The usual choice for a daemon.
	Session Detach = iota
	// ProcessGroup starts the child in a new process group but keeps the
	// parent's session and controlling terminal (setpgid).
	ProcessGroup
)

// SpawnOptions configures [Spawn].
type SpawnOptions struct {
	// Args are the arguments after the executable, e.g. {"daemon", "run"}.
	Args []string
	// Env is appended to the parent's environment.
	Env []string
	// Detach defaults to Session.
	Detach Detach
	// Stdout and Stderr receive the child's output; nil means the null
	// device. The caller keeps ownership of the files and may close them once
	// Spawn returns. Stdin is always the null device.
	Stdout *os.File
	Stderr *os.File
}
