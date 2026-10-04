package procuse

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func record(pid int, fd, path string) []byte {
	return []byte(fmt.Sprintf("p%d\x00\nf%s\x00n%s\x00\n", pid, fd, path))
}

type reply struct {
	stdout, stderr []byte
	err            error
}

type fakeRunner struct {
	t                       *testing.T
	cwd, target             string
	calls                   [][]string
	controlCwd, controlFile *reply
	targetCwd, targetOpen   reply
}

func (f *fakeRunner) run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	f.t.Helper()
	if _, bounded := ctx.Deadline(); !bounded {
		f.t.Fatal("unbounded lsof call")
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	cwdOnly := len(args) == 6
	var path string
	if cwdOnly {
		if !reflect.DeepEqual(args[:5], []string{"-a", "-d", "cwd", "-F0pfn", "+D"}) {
			f.t.Fatalf("unexpected cwd command %v", args)
		}
		path = args[5]
	} else {
		if len(args) != 3 || !reflect.DeepEqual(args[:2], []string{"-F0pfn", "+D"}) {
			f.t.Fatalf("unexpected open command %v", args)
		}
		path = args[2]
	}
	var r reply
	switch {
	case path == f.target:
		if cwdOnly {
			r = f.targetCwd
		} else {
			r = f.targetOpen
		}
	case cwdOnly && path == f.cwd:
		r.stdout = record(os.Getpid(), "cwd", path)
		if f.controlCwd != nil {
			r = *f.controlCwd
		}
	case !cwdOnly && strings.HasPrefix(filepath.Base(path), "procuse-control-"):
		file := filepath.Join(path, "held")
		if _, err := os.Stat(file); err != nil {
			f.t.Fatal("missing open-file control", err)
		}
		r.stdout = record(os.Getpid(), "3", file)
		if f.controlFile != nil {
			r = *f.controlFile
		}
	default:
		f.t.Fatalf("unexpected target %q", path)
	}
	return r.stdout, r.stderr, r.err
}

func fixture(t *testing.T) (*fakeRunner, Options) {
	t.Helper()
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "candidate")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	return &fakeRunner{t: t, cwd: cwd, target: target}, Options{ScratchDir: root}
}

func check(t *testing.T, f *fakeRunner, options Options) Result {
	t.Helper()
	result := Check(context.Background(), f.target, f.run, options)
	entries, err := os.ReadDir(options.ScratchDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "procuse-control-") {
			t.Fatal("control scratch leaked")
		}
	}
	return result
}

func TestCheckThreeOutcomes(t *testing.T) {
	for _, kind := range []string{"free", "cwd", "subdirectory cwd", "open fd", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			f, options := fixture(t)
			want := InUse
			switch kind {
			case "free":
				want = NotInUse
			case "cwd":
				f.targetCwd.stdout = record(123, "cwd", f.target)
			case "subdirectory cwd":
				f.targetCwd.stdout = record(123, "cwd", filepath.Join(f.target, "nested"))
			case "open fd":
				f.targetOpen.stdout = record(123, "9", filepath.Join(f.target, "config.json"))
			case "malformed":
				f.targetOpen.stdout = []byte("garbage")
				want = Unknown
			}
			result := check(t, f, options)
			if result.Outcome != want {
				t.Fatalf("result = %+v; want %v", result, want)
			}
		})
	}
}

func TestCheckFreshControlsAndOrdering(t *testing.T) {
	f, options := fixture(t)
	for i := 0; i < 2; i++ {
		if got := check(t, f, options); got.Outcome != NotInUse {
			t.Fatal(got)
		}
		calls := f.calls[i*4 : (i+1)*4]
		if calls[0][5] != f.cwd || !strings.HasPrefix(filepath.Base(calls[1][2]), "procuse-control-") || calls[2][5] != f.target || calls[3][2] != f.target {
			t.Fatalf("probe ordering = %v", calls)
		}
	}
	if f.calls[1][2] == f.calls[5][2] {
		t.Fatal("open-file control reused across checks")
	}
	// A formerly working mechanism becoming blind must not reuse cached proof.
	f.controlFile = &reply{}
	if got := check(t, f, options); got.Outcome != Unknown {
		t.Fatal(got)
	}
}

func TestCheckFailedControlsRetain(t *testing.T) {
	for _, control := range []string{"cwd", "file"} {
		for _, kind := range []string{"empty", "other process", "wrong path", "wrong descriptor", "malformed", "error", "stderr"} {
			t.Run(control+"/"+kind, func(t *testing.T) {
				f, options := fixture(t)
				r := reply{}
				switch kind {
				case "other process":
					r.stdout = record(os.Getpid()+1, "cwd", f.cwd)
				case "wrong path":
					r.stdout = record(os.Getpid(), "cwd", filepath.Dir(f.cwd))
				case "wrong descriptor":
					r.stdout = record(os.Getpid(), "7", f.cwd)
				case "malformed":
					r.stdout = []byte("p1\x00\n")
				case "error":
					r.err = exec.ErrNotFound
				case "stderr":
					r.stderr = []byte("warning")
				}
				if control == "cwd" {
					f.controlCwd = &r
				} else {
					f.controlFile = &r
				}
				got := check(t, f, options)
				if got.Outcome != Unknown || got.Reason == "" {
					t.Fatalf("result=%+v", got)
				}
				for _, args := range f.calls {
					if args[len(args)-1] == f.target {
						t.Fatal("target probed after failed control")
					}
				}
			})
		}
	}
}

func TestCheckTargetFailuresRetain(t *testing.T) {
	for _, mode := range []string{"cwd", "open"} {
		for _, kind := range []string{"exec", "stderr", "malformed", "outside", "unexpected fd"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				f, options := fixture(t)
				r := reply{}
				switch kind {
				case "exec":
					r.err = &exec.Error{Name: "lsof", Err: exec.ErrNotFound}
				case "stderr":
					r.stderr = []byte("visibility warning")
				case "malformed":
					r.stdout = []byte("COMMAND\n")
				case "outside":
					r.stdout = record(123, "cwd", filepath.Dir(f.target))
				case "unexpected fd":
					r.stdout = record(123, "NOFD", f.target)
				}
				if mode == "cwd" {
					f.targetCwd = r
				} else {
					f.targetOpen = r
					f.targetCwd.stdout = record(123, "cwd", f.target)
				}
				if got := check(t, f, options); got.Outcome != Unknown {
					t.Fatal(got)
				}
			})
		}
	}
}

func TestCheckCancellationAndDeadline(t *testing.T) {
	for _, mode := range []string{"control cwd", "control open", "target cwd", "target open"} {
		t.Run(mode, func(t *testing.T) {
			f, options := fixture(t)
			options.Timeout = time.Millisecond
			call := 0
			wantCall := map[string]int{"control cwd": 1, "control open": 2, "target cwd": 3, "target open": 4}[mode]
			runner := func(ctx context.Context, args ...string) ([]byte, []byte, error) {
				call++
				if call == wantCall {
					<-ctx.Done()
					return nil, nil, ctx.Err()
				}
				return f.run(ctx, args...)
			}
			got := Check(context.Background(), f.target, runner, options)
			if got.Outcome != Unknown || !strings.Contains(got.Reason, "deadline") {
				t.Fatal(got)
			}
		})
	}
	f, options := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Check(ctx, f.target, f.run, options); got.Outcome != Unknown {
		t.Fatal(got)
	}
}

func TestCheckRequiresResources(t *testing.T) {
	f, options := fixture(t)
	if got := Check(context.Background(), f.target, f.run, Options{ScratchDir: f.target}); got.Outcome != Unknown {
		t.Fatal(got)
	}
	for _, bad := range []string{"", "relative", "/fixture\nroot", "/fixture\x00root"} {
		if got := Check(context.Background(), bad, f.run, options); got.Outcome != Unknown {
			t.Fatal(got)
		}
		if got := Check(context.Background(), f.target, f.run, Options{ScratchDir: bad}); got.Outcome != Unknown {
			t.Fatal(got)
		}
	}
	if got := Check(context.Background(), f.target, nil, options); got.Outcome != Unknown {
		t.Fatal(got)
	}
	if got := Check(context.Background(), f.target, f.run, Options{ScratchDir: filepath.Join(options.ScratchDir, "absent")}); got.Outcome != Unknown {
		t.Fatal(got)
	}
}

func TestProbeExitStatusSanity(t *testing.T) {
	for _, exitCode := range []int{1, 2, -1} {
		f, options := fixture(t)
		f.targetOpen.err = &exec.ExitError{ProcessState: testProcessState(t, exitCode)}
		got := check(t, f, options)
		if exitCode == 1 && got.Outcome != NotInUse || exitCode != 1 && got.Outcome != Unknown {
			t.Fatalf("exit %d: %+v", exitCode, got)
		}
	}
}

// ProcessState cannot be constructed by Go callers. A tiny child of this test
// binary supplies real exit statuses without depending on a shell command.
func testProcessState(t *testing.T, code int) *os.ProcessState {
	t.Helper()
	if code < 0 {
		return nil
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestExitStatusHelper$")
	cmd.Env = append(os.Environ(), "PROCUSE_TEST_EXIT="+strconv.Itoa(code))
	var exit *exec.ExitError
	err := cmd.Run()
	if !errors.As(err, &exit) {
		t.Fatalf("exit helper: %v", err)
	}
	return exit.ProcessState
}

func TestExitStatusHelper(t *testing.T) {
	if value := os.Getenv("PROCUSE_TEST_EXIT"); value != "" {
		code, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(code)
	}
}

func TestParseStructuredRecords(t *testing.T) {
	good := append(record(123, "cwd", "/fixture/root"), record(456, "9", "/fixture/root/sub/file")...)
	refs, ok := parseRecords(good)
	if !ok || len(refs) != 2 || refs[1].pid != 456 || refs[1].name != "/fixture/root/sub/file" {
		t.Fatalf("refs=%v ok=%v", refs, ok)
	}
	if refs, ok := parseRecords(nil); !ok || len(refs) != 0 {
		t.Fatal("empty output rejected")
	}
	for _, bad := range []string{
		" ", "COMMAND PID\n", "p1\x00", "p1\n", "p0\x00\nfcwd\x00n/fixture/root\x00\n",
		"p-1\x00\n", "p999999999999999999999999\x00\n", "p1\x00\n", "fcwd\x00n/fixture/root\x00\n",
		"p1\x00cproc\x00\nfcwd\x00n/fixture/root\x00\n", "p1\x00\np2\x00\nfcwd\x00n/fixture/root\x00\n",
		"p1\x00\nfcwd\x00\n", "p1\x00\nfcwd\x00nrelative\x00\n", "p1\x00\nfcwd\x00n/fixture/root\x00n/other\x00\n",
		"p1\x00\nfNOFD\x00n/fixture/root\x00\n", "p1\x00\nferr\x00n/fixture/root\x00\n", "p1\x00\nfweird\x00n/fixture/root\x00\n",
		"p1\x00\nfcwd\x00n/fixture/root\r\x00\n", "p1\x00\nfcwd\x00n/fixture/root\x00\n\n",
	} {
		if _, ok := parseRecords([]byte(bad)); ok {
			t.Errorf("accepted malformed records %q", bad)
		}
	}
}

func TestProbeRejectsUnexpectedSelection(t *testing.T) {
	runner := func(context.Context, ...string) ([]byte, []byte, error) {
		return record(123, "7", "/fixture/root/file"), nil, nil
	}
	if _, reason := probe(context.Background(), runner, "/fixture/root", true, time.Second); reason == "" {
		t.Fatal("cwd selection accepted numeric fd")
	}
}

func TestTimeoutBounds(t *testing.T) {
	f, options := fixture(t)
	for _, value := range []time.Duration{0, -time.Second, time.Minute} {
		options.Timeout = value
		runner := func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Fatal("timeout not capped")
			}
			return f.run(ctx, args...)
		}
		if got := Check(context.Background(), f.target, runner, options); got.Outcome != NotInUse {
			t.Fatal(got)
		}
	}
}

func TestOutcomeNamesAndZeroValue(t *testing.T) {
	if (Result{}).Outcome != Unknown {
		t.Fatal("zero result must retain")
	}
	for outcome, name := range map[Outcome]string{Unknown: "UNKNOWN", InUse: "in-use", NotInUse: "not-in-use"} {
		if outcome.String() != name {
			t.Fatal(outcome, name)
		}
	}
}

// One real-tool test proves both reference modes and the free-path result.
// It uses only this process's cwd, held fixture files and private scratch.
func TestRealLsofControlsAndReferences(t *testing.T) {
	path, err := exec.LookPath("lsof")
	if err != nil {
		t.Skip("lsof absent: real process-use visibility test unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, "scratch")
	candidate := filepath.Join(root, "candidate")
	free := filepath.Join(root, "free")
	for _, dir := range []string{scratch, candidate, free} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	held, err := os.Create(filepath.Join(candidate, "open.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	runner := ExecRunner(path)
	options := Options{ScratchDir: scratch}
	if got := Check(context.Background(), candidate, runner, options); got.Outcome != InUse {
		t.Fatalf("open reference: %+v", got)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if got := Check(context.Background(), candidate, runner, options); got.Outcome != NotInUse {
		t.Fatalf("released reference: %+v", got)
	}
	if got := Check(context.Background(), free, runner, options); got.Outcome != NotInUse {
		t.Fatalf("free: %+v", got)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := Check(context.Background(), cwd, runner, options); got.Outcome != InUse {
		t.Fatalf("cwd: %+v", got)
	}
	if got := Check(context.Background(), candidate, ExecRunner(filepath.Join(root, "missing-lsof")), options); got.Outcome != Unknown {
		t.Fatalf("missing tool: %+v", got)
	}
}

func TestCheckLeavesTargetUntouched(t *testing.T) {
	f, options := fixture(t)
	marker := filepath.Join(f.target, "config.json")
	if err := os.WriteFile(marker, []byte("owned fixture content"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := check(t, f, options); got.Outcome != NotInUse {
		t.Fatal(got)
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "owned fixture content" {
		t.Fatalf("target changed: %q, %v", content, err)
	}
}

func TestExecRunnerMissingToolRetains(t *testing.T) {
	f, options := fixture(t)
	missing := filepath.Join(options.ScratchDir, "missing-lsof")
	if got := Check(context.Background(), f.target, ExecRunner(missing), options); got.Outcome != Unknown {
		t.Fatal(got)
	}
}
