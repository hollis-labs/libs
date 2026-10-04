package procuse

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Outcome has three states. The zero value is Unknown and requires retention.
type Outcome uint8

const (
	Unknown Outcome = iota
	InUse
	NotInUse
)

func (o Outcome) String() string {
	switch o {
	case InUse:
		return "in-use"
	case NotInUse:
		return "not-in-use"
	default:
		return "UNKNOWN"
	}
}

// Result describes an observation, never permission to remove or replant.
// Reason names a failure class without including raw command output.
type Result struct {
	Outcome Outcome
	Reason  string
	Code    ReasonCode
	proof   bool
}

// ReasonCode identifies a preflight refusal without including host paths.
type ReasonCode string

const (
	ReasonNonCanonicalPath      ReasonCode = "non-canonical-path"
	ReasonDeviceMismatch        ReasonCode = "device-mismatch"
	ReasonUnsupportedFilesystem ReasonCode = "unsupported-filesystem"
	ReasonUnsupportedPlatform   ReasonCode = "unsupported-platform"
)

// SafeToClean reports an intact, scoped negative observation earned by Check.
// A hand-built Result is never proof. This method does not authorize removal:
// ownership, retention, pin-lock proof and launch coordination remain required.
func (r Result) SafeToClean() bool {
	return r.proof && r.Outcome == NotInUse && r.Reason == "" && r.Code == ""
}

// Runner executes lsof directly and captures stdout and stderr separately.
// Implementations must honor context cancellation, including pipe shutdown.
type Runner func(context.Context, ...string) (stdout, stderr []byte, err error)

// Options binds private control storage and the timeout of each invocation.
// ScratchDir is required, canonical, absolute, caller-authorized storage outside
// the target. A nonpositive Timeout uses five seconds; larger values are capped
// at five seconds.
type Options struct {
	ScratchDir string
	Timeout    time.Duration
}

// Check runs fresh cwd and open-file positive controls, then both target
// probes. An empty result is meaningful only after both controls succeed.
// Each command has its own deadline; the parent context bounds the whole check.
// With the real runner, four commands take at most about 4 x (5s + 1s),
// excluding filesystem preflight and control creation/cleanup.
// Any failed command sanity check, stderr, invalid record or failed control is
// Unknown, even when another probe found a reference. lsof exit status 1 alone
// is accepted after output validation; other nonzero statuses are uncertain.
func Check(ctx context.Context, target string, runner Runner, options Options) (result Result) {
	unknown := func(reason string) Result { return Result{Outcome: Unknown, Reason: reason} }
	if ctx == nil {
		return unknown("context is required")
	}
	if runner == nil || !safeAbsolute(target) || !safeAbsolute(options.ScratchDir) {
		return unknown("runner, absolute paths and scratch outside target are required")
	}
	targetRoot, ok := canonicalDirectory(target)
	if !ok {
		return Result{Outcome: Unknown, Code: ReasonNonCanonicalPath, Reason: "non-canonical path"}
	}
	scratchRoot, ok := canonicalDirectory(options.ScratchDir)
	if !ok {
		return Result{Outcome: Unknown, Code: ReasonNonCanonicalPath, Reason: "non-canonical path"}
	}
	target, options.ScratchDir = targetRoot, scratchRoot
	if within(target, scratchRoot) {
		return unknown("scratch must be outside target")
	}
	if code := platformRoots(target, scratchRoot); code != "" {
		return Result{Outcome: Unknown, Code: code, Reason: string(code)}
	}
	// Canonicalize cwd only for identity comparison; never walk its descendants.
	cwd, err := os.Getwd()
	if err != nil {
		return unknown("caller cwd unavailable")
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return unknown("caller cwd unavailable")
	}
	controlDir, err := os.MkdirTemp(options.ScratchDir, "procuse-control-")
	if err != nil {
		return unknown("control scratch unavailable")
	}
	defer func() {
		if err := os.Remove(controlDir); err != nil {
			result = unknown("control directory cleanup failed")
		}
	}()
	controlPath := filepath.Join(controlDir, "held")
	file, err := os.OpenFile(controlPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return unknown("open-file control unavailable")
	}
	defer func() {
		closeErr := file.Close()
		removeErr := os.Remove(controlPath)
		if closeErr != nil || removeErr != nil {
			result = unknown("control file cleanup failed")
		}
	}()

	timeout := options.Timeout
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	cwdControl, reason := probeSelection(ctx, runner, cwd, true, os.Getpid(), timeout)
	if reason != "" {
		return unknown("cwd control: " + reason)
	}
	if !hasReference(cwdControl, os.Getpid(), "cwd", cwd) {
		return unknown("cwd control did not observe caller")
	}
	fileControl, reason := probe(ctx, runner, controlDir, false, timeout)
	if reason != "" {
		return unknown("open-file control: " + reason)
	}
	if !hasReference(fileControl, os.Getpid(), "", controlPath) {
		return unknown("open-file control did not observe caller-held file")
	}
	cwdRefs, reason := probe(ctx, runner, target, true, timeout)
	if reason != "" {
		return unknown("target cwd probe: " + reason)
	}
	openRefs, reason := probe(ctx, runner, target, false, timeout)
	if reason != "" {
		return unknown("target open-reference probe: " + reason)
	}
	if len(cwdRefs) > 0 || len(openRefs) > 0 {
		return Result{Outcome: InUse}
	}
	return Result{Outcome: NotInUse, proof: true}
}

func canonicalDirectory(path string) (string, bool) {
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != filepath.Clean(path) {
		return "", false
	}
	info, err := os.Stat(physical)
	return physical, err == nil && info.IsDir()
}

func safeAbsolute(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsAny(path, "\x00\r\n")
}

type reference struct {
	pid  int
	fd   string
	name string
}

func hasReference(refs []reference, pid int, fd, name string) bool {
	for _, ref := range refs {
		if ref.pid == pid && (ref.fd == fd || fd == "" && digits(ref.fd)) && ref.name == name {
			return true
		}
	}
	return false
}

func probe(ctx context.Context, runner Runner, target string, cwdOnly bool, timeout time.Duration) ([]reference, string) {
	return probeSelection(ctx, runner, target, cwdOnly, 0, timeout)
}

func probeSelection(ctx context.Context, runner Runner, target string, cwdOnly bool, pid int, timeout time.Duration) ([]reference, string) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := probeArgs(target, cwdOnly, pid)
	stdout, stderr, err := runner(callCtx, args...)
	if callCtx.Err() != nil {
		return nil, "deadline exceeded or canceled"
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, "command failed"
		}
	}
	if len(stdout) > outputLimit || len(stderr) > outputLimit {
		return nil, "output limit exceeded"
	}
	if len(stderr) != 0 {
		return nil, "stderr output"
	}
	refs, ok := parseRecords(stdout)
	if !ok {
		return nil, "invalid structured output"
	}
	for _, ref := range refs {
		if cwdOnly && ref.fd != "cwd" || !within(target, ref.name) {
			return nil, "unexpected reference"
		}
	}
	return refs, ""
}

func probeArgs(target string, cwdOnly bool, pid int) []string {
	args := []string{"-F0pfn"}
	if cwdOnly {
		args = append([]string{"-a", "-d", "cwd"}, args...)
	}
	if pid > 0 {
		return append(args, "-p", strconv.Itoa(pid))
	}
	return append(args, "+D", target)
}

func within(root, name string) bool {
	// lsof can append this annotation to an absolute pathname.
	name = strings.TrimSuffix(name, " (deleted)")
	rel, err := filepath.Rel(root, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// parseRecords accepts only the requested p/f/n schema. A process set starts
// with p; each file set starts with f and contains exactly one absolute n.
// Fields end with NUL and sets with newline. Incomplete or unexpected sets
// fail closed. Empty output is the only no-reference representation accepted.
func parseRecords(output []byte) ([]reference, bool) {
	if len(output) == 0 {
		return nil, true
	}
	if output[len(output)-1] != '\n' {
		return nil, false
	}
	var refs []reference
	pid, files := 0, 0
	for _, line := range strings.Split(string(output[:len(output)-1]), "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) < 2 || fields[len(fields)-1] != "" {
			return nil, false
		}
		fields = fields[:len(fields)-1]
		if len(fields[0]) < 2 {
			return nil, false
		}
		switch fields[0][0] {
		case 'p':
			if len(fields) != 1 || pid != 0 && files == 0 || !digits(fields[0][1:]) {
				return nil, false
			}
			var err error
			pid, err = strconv.Atoi(fields[0][1:])
			if err != nil || pid <= 0 {
				return nil, false
			}
			files = 0
		case 'f':
			if pid == 0 || len(fields) != 2 || !validFD(fields[0][1:]) ||
				len(fields[1]) < 2 || fields[1][0] != 'n' {
				return nil, false
			}
			name, ok := unescapeName(fields[1][1:])
			if !ok || !filepath.IsAbs(name) || strings.ContainsRune(name, 0) {
				return nil, false
			}
			refs = append(refs, reference{pid: pid, fd: fields[0][1:], name: name})
			files++
		default:
			return nil, false
		}
	}
	return refs, pid != 0 && files > 0
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validFD(fd string) bool {
	if digits(fd) {
		return true
	}
	switch fd {
	case "cwd", "rtd", "txt", "mem", "mmap", "ltx", "ctty", "jld", "pd", "twd", "m86", "v86", "DEL":
		return true
	default:
		// Unsupported descriptor dialects and lsof error descriptors retain.
		return false
	}
}
