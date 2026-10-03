package shim

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

// Launch is already authorized host configuration. Env is the complete child
// environment, never an ambient merge. The host must hide ControlDir and the
// launch descriptor from untrusted children using its sandbox bindings.
type Launch struct {
	Session        string        `json:"session"`
	Instance       string        `json:"instance"`
	Generation     uint64        `json:"generation,string"`
	Actor          mesh.Actor    `json:"actor"`
	Subject        mesh.URN      `json:"subject"`
	Argv           []string      `json:"argv"`
	Env            []string      `json:"env"`
	Cwd            string        `json:"cwd"`
	ControlDir     string        `json:"control_dir"`
	JournalDir     string        `json:"journal_dir"`
	Secret         string        `json:"secret"`
	PinPath        string        `json:"pin_path"`
	PinKey         string        `json:"pin_key"`
	BootGeneration string        `json:"boot_generation"`
	Reservation    string        `json:"reservation"`
	JournalBytes   int64         `json:"journal_bytes"`
	WallTime       time.Duration `json:"wall_time_ns"`
	StopGrace      time.Duration `json:"stop_grace_ns"`
	Heartbeat      time.Duration `json:"heartbeat_ns"`
	ClientQueue    int           `json:"client_queue"`
}

type Exit struct {
	Signal int    `json:"signal,omitempty"`
	Status int    `json:"status"`
	Cause  string `json:"cause"`
}

type Host struct {
	driver          transportDriver
	connectionSlots chan struct{}
	launch          Launch
	journal         *Journal
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	pin             *os.File
	listener        *net.UnixListener
	done            chan struct{}
	closing         chan struct{}
	once            sync.Once
	wg              sync.WaitGroup
	mu              sync.Mutex
	processMu       sync.Mutex
	reaped          bool
	exit            Exit
	running         bool
	cause           string
	epoch           uint64
	controller      *connection
	observer        *connection
	// Operations serialize durable intent/effect/outcome, including takeover.
	op       sync.Mutex
	receipts map[string]receipt
}

func Start(spec Launch) (*Host, error) {
	if len(filepath.Join(spec.ControlDir, "control.sock")) >= 104 {
		return nil, fault("invalid_request", "control socket path exceeds portable Unix limit")
	}
	if spec.Session == "" || spec.Instance == "" || spec.Generation == 0 || len(spec.Argv) == 0 || spec.Argv[0] == "" || len(spec.Secret) < 32 || len(spec.Session) > 1024 || len(spec.Instance) > 1024 || len(spec.Actor.URN) > 1024 || len(spec.Subject) > 1024 || spec.PinKey == "" || spec.BootGeneration == "" || spec.Reservation == "" {
		return nil, fault("invalid_request", "incomplete launch identity, capability or pin")
	}
	if err := spec.Actor.Validate(); err != nil {
		return nil, err
	}
	if err := spec.Subject.Validate(); err != nil {
		return nil, err
	}
	for _, p := range []string{spec.ControlDir, spec.JournalDir, spec.PinPath, spec.Cwd, spec.Argv[0]} {
		if !filepath.IsAbs(p) {
			return nil, fault("invalid_request", "launch paths must be absolute")
		}
	}
	if spec.JournalBytes == 0 {
		spec.JournalBytes = 16 << 20
	}
	if spec.StopGrace == 0 {
		spec.StopGrace = 2 * time.Second
	}
	if spec.Heartbeat == 0 {
		spec.Heartbeat = 10 * time.Second
	}
	if spec.ClientQueue == 0 {
		spec.ClientQueue = 64
	}
	if spec.WallTime < 0 || spec.StopGrace < 0 || spec.Heartbeat < 0 || spec.ClientQueue < 1 || spec.ClientQueue > 1024 {
		return nil, fault("invalid_request", "invalid launch limits")
	}
	if err := privateDir(spec.ControlDir); err != nil {
		return nil, err
	}
	j, err := OpenJournal(spec.JournalDir, spec.Session, spec.Generation, spec.JournalBytes)
	if err != nil {
		return nil, err
	}
	// Restarting a host is not reconnecting. Even an uncertain launch intent
	// refuses a second spawn; the caller must inspect the original process.
	for _, e := range j.Snapshot() {
		if e.Kind == "shim.launch_intent" {
			j.Close()
			return nil, fault("outcome_unknown", "existing launch requires inspection, not respawn")
		}
	}
	h := &Host{launch: spec, journal: j, done: make(chan struct{}), closing: make(chan struct{}), receipts: map[string]receipt{}, connectionSlots: make(chan struct{}, 8)}
	cleanup := func() {
		if h.stdin != nil {
			h.stdin.Close()
		}
		if h.listener != nil {
			h.listener.Close()
		}
		if h.pin != nil {
			h.pin.Close()
		}
		j.Close()
	}
	h.pin, err = lockExistingPin(spec.PinPath)
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err = h.record("shim.pin_adopted", map[string]string{"pin_key": spec.PinKey, "boot_generation": spec.BootGeneration, "reservation": spec.Reservation}, false); err != nil {
		cleanup()
		return nil, err
	}
	socket := filepath.Join(spec.ControlDir, "control.sock")
	// Never unlink somebody else's socket. The caller supplies a fresh private
	// control directory for each hosted runtime incarnation.
	h.listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		cleanup()
		return nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		cleanup()
		return nil, err
	}
	h.cmd = exec.Command(spec.Argv[0], spec.Argv[1:]...)
	h.cmd.Dir = spec.Cwd
	h.cmd.Env = append([]string{}, spec.Env...)
	h.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	h.stdin, err = h.cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	// Explicit pipes let readers drain after Wait without exec.Wait closing
	// their descriptors while output remains unread.
	outR, outW, err := os.Pipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		cleanup()
		return nil, err
	}
	h.cmd.Stdout = outW
	h.cmd.Stderr = errW
	if _, err = h.record("shim.launch_intent", map[string]string{"instance": spec.Instance}, false); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		h.stdin.Close()
		cleanup()
		return nil, err
	}
	err = h.cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		h.stdin.Close()
		h.record("shim.exit", Exit{Status: -1, Cause: "spawn_failed"}, true)
		cleanup()
		return nil, fault("spawn_failed", "could not start child")
	}
	h.driver = &stdioDriver{input: h.stdin, pid: h.cmd.Process.Pid}
	h.running = true
	if _, err = h.record("shim.started", map[string]int{"pid": h.cmd.Process.Pid}, false); err != nil {
		h.cause = "journal_failure"
		gap := h.event("shim.output_gap", map[string]string{"code": codeOf(err)})
		gap.Truncated = true
		h.journal.Append(gap, true)
		h.sendSignal(syscall.SIGKILL)
	}
	var readers sync.WaitGroup
	readers.Add(2)
	for _, stream := range []struct {
		name string
		r    *os.File
	}{{"stdout", outR}, {"stderr", errR}} {
		go func(name string, r *os.File) { defer readers.Done(); defer r.Close(); h.capture(name, r) }(stream.name, stream.r)
	}
	h.wg.Add(1)
	go h.accept()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		noticeErr := waitLeaderExit(h.cmd.Process.Pid)
		if noticeErr != nil {
			h.sendSignal(syscall.SIGKILL)
		}
		h.processMu.Lock()
		if noticeErr == nil {
			signalGroup(h.cmd.Process.Pid, syscall.SIGKILL)
		}
		waitErr := h.cmd.Wait()
		h.reaped = true
		h.processMu.Unlock()
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
		h.stdin.Close()
		drained := make(chan struct{})
		go func() { readers.Wait(); close(drained) }()
		drainTimer := time.NewTimer(150 * time.Millisecond)
		drainGap := false
		select {
		case <-drained:
		case <-drainTimer.C:
			drainGap = true
			outR.Close()
			errR.Close()
			<-drained
		}
		drainTimer.Stop()
		if drainGap {
			gap := h.event("shim.output_gap", map[string]string{"code": "descendant_holds_pipe"})
			gap.Truncated = true
			h.journal.Append(gap, true)
		}
		status := h.cmd.ProcessState.ExitCode()
		cause := "exit"
		if waitErr != nil {
			cause = "child_failed"
		}
		if drainGap {
			cause = "descendant_holds_pipe"
		}
		h.mu.Lock()
		if h.cause != "" {
			cause = h.cause
		}
		h.exit = Exit{Status: status, Cause: cause}
		if ws, ok := h.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			h.exit.Signal = int(ws.Signal())
		}
		h.running = false
		h.mu.Unlock()
		h.record("shim.exit", h.exit, true)
		h.pin.Close()
		close(h.done)
	}()
	if spec.WallTime > 0 {
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			timer := time.NewTimer(spec.WallTime)
			defer timer.Stop()
			select {
			case <-timer.C:
				h.stop("wall_time")
			case <-h.done:
			}
		}()
	}
	return h, nil
}
func (h *Host) event(kind string, v any) mesh.Event {
	return mesh.Event{SchemaVersion: "1", ID: newID(), Kind: kind, Time: time.Now().UTC(), App: "cairn-shim", SessionID: h.launch.Session, Source: mesh.EventSource{Channel: "shim", Confidence: 1}, Actor: h.launch.Actor, Subject: h.launch.Subject, Generation: h.launch.Generation, ContentType: "application/json", PayloadSchema: "shim/v1", Visibility: "private", Payload: body(v)}
}
func (h *Host) record(kind string, v any, terminal bool) (mesh.Event, error) {
	return h.journal.Append(h.event(kind, v), terminal)
}
func (h *Host) capture(name string, r io.Reader) {
	b := make([]byte, OutputChunk)
	for {
		n, err := r.Read(b)
		if n > 0 {
			_, e := h.record("shim.output", map[string]string{"stream": name, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(b[:n])}, false)
			if e != nil {
				// Output cannot continue without durable capture, even if a future
				// envelope bug reports a normally nonfatal request-validation error.
				h.failJournal(fault("journal_unavailable", "output append failed: "+codeOf(e)))
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				gap := h.event("shim.output_gap", map[string]string{"stream": name})
				gap.Truncated = true
				h.journal.Append(gap, true)
			}
			return
		}
	}
}
func (h *Host) failJournal(err error) {
	if codeOf(err) != "journal_unavailable" && codeOf(err) != "journal_full" {
		return
	}
	e := h.event("shim.output_gap", map[string]string{"code": codeOf(err)})
	e.Truncated = true
	h.journal.Append(e, true)
	h.stop("journal_failure")
}
func (h *Host) stop(cause string) {
	h.mu.Lock()
	running := h.running
	if running && h.cause == "" {
		h.cause = cause
	}
	h.mu.Unlock()
	h.processMu.Lock()
	reaped := h.reaped
	h.processMu.Unlock()
	if !running || reaped {
		return
	}
	h.sendSignal(syscall.SIGTERM)
	timer := time.NewTimer(h.launch.StopGrace)
	defer timer.Stop()
	select {
	case <-h.done:
	case <-timer.C:
		h.sendSignal(syscall.SIGKILL)
	}
}
func (h *Host) Done() <-chan struct{} { return h.done }
func (h *Host) Wait() Exit            { <-h.done; h.mu.Lock(); defer h.mu.Unlock(); return h.exit }
func (h *Host) SocketPath() string    { return filepath.Join(h.launch.ControlDir, "control.sock") }
func (h *Host) Close() error {
	h.once.Do(func() {
		close(h.closing)
		h.listener.Close()
		h.mu.Lock()
		if h.controller != nil {
			h.controller.close()
		}
		if h.observer != nil {
			h.observer.close()
		}
		h.mu.Unlock()
		h.stop("host_shutdown")
		h.wg.Wait()
		h.journal.Close()
	})
	return nil
}
func (h *Host) health() any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return struct {
		Running    bool   `json:"running"`
		PID        int    `json:"pid"`
		Exit       Exit   `json:"exit"`
		Cursor     string `json:"cursor"`
		Generation string `json:"generation"`
	}{h.running, h.cmd.Process.Pid, h.exit, h.journal.HighWater(), fmt.Sprint(h.launch.Generation)}
}

// ReadLaunch requires a private regular descriptor. It does not merge ambient
// environment variables or infer resources from the operator's home.
func ReadLaunch(path string) (Launch, error) {
	var launch Launch
	info, err := os.Lstat(path)
	if err != nil {
		return launch, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return launch, fault("unsafe_path", "launch descriptor must be a private regular file")
	}
	f, err := openPrivateFile(path, syscall.O_RDONLY)
	if err != nil {
		return launch, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, MaxFrame+1)).Decode(&launch)
	return launch, err
}

func (h *Host) sendSignal(s syscall.Signal) error {
	h.processMu.Lock()
	defer h.processMu.Unlock()
	if h.reaped {
		return fault("target_offline", "child reaped")
	}
	return h.driver.SendSignal(s)
}
