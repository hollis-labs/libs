package conformance

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
	"github.com/hollis-labs/go-hooks/cmdhook"
)

// FixtureFS holds the conformance fixture tree, rooted at Root.
//
//go:embed testdata
var FixtureFS embed.FS

// Root is the directory inside FixtureFS that holds the cases.
const Root = "testdata"

const (
	scriptName = "hook.sh"
	inputName  = "input.json"
	wantName   = "want.json"

	caseTimeout = 10 * time.Second
)

// Want is the expected outcome of one case.
type Want struct {
	// ExitCode is the exit code the script produces: 0 success, 2 block,
	// anything else a failure.
	ExitCode int
	// Output is the expected decoded Output, or nil when the case must
	// fail.
	Output *hooks.Output
	// ErrSubstr, when non-empty, must appear in the returned error text.
	ErrSubstr string
}

// Case is one fixture.
type Case struct {
	// Dir is the case directory within the file system passed to Load, for
	// example testdata/PreToolUse/deny-rm.
	Dir   string
	Event hooks.Event
	// Hook is the registration used to run the case. Load leaves Command
	// empty; Run fills it with the extracted Script. A host that supplies
	// its own Command keeps it.
	Hook hooks.Hook
	// Input is the JSON written to the hook's stdin.
	Input json.RawMessage
	// Script is the content of the case's hook.sh.
	Script []byte
	Want   Want
}

type wantFile struct {
	ExitCode  int           `json:"exit_code"`
	Output    *hooks.Output `json:"output"`
	ErrSubstr string        `json:"want_err_substr"`
}

// Load walks fsys under root, expecting root/<Event>/<case>/ directories, and
// returns every Case sorted by directory. A nil fsys means FixtureFS. It
// rejects an unknown event directory, an input whose hook_event_name does
// not match its directory, and a case missing any of its three files.
func Load(fsys fs.FS, root string) ([]Case, error) {
	if fsys == nil {
		fsys = FixtureFS
	}
	events, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("conformance: read %s: %w", root, err)
	}
	var cases []Case
	for _, ev := range events {
		if !ev.IsDir() {
			continue
		}
		event := hooks.Event(ev.Name())
		if !event.Valid() {
			return nil, fmt.Errorf("conformance: %s/%s is not a core event", root, ev.Name())
		}
		entries, err := fs.ReadDir(fsys, path.Join(root, ev.Name()))
		if err != nil {
			return nil, fmt.Errorf("conformance: %w", err)
		}
		for _, ce := range entries {
			if !ce.IsDir() {
				continue
			}
			c, err := loadCase(fsys, path.Join(root, ev.Name(), ce.Name()), event)
			if err != nil {
				return nil, err
			}
			cases = append(cases, c)
		}
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Dir < cases[j].Dir })
	return cases, nil
}

func loadCase(fsys fs.FS, dir string, event hooks.Event) (Case, error) {
	read := func(name string) ([]byte, error) {
		b, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("conformance: case %s: %w", dir, err)
		}
		return b, nil
	}
	script, err := read(scriptName)
	if err != nil {
		return Case{}, err
	}
	input, err := read(inputName)
	if err != nil {
		return Case{}, err
	}
	wantRaw, err := read(wantName)
	if err != nil {
		return Case{}, err
	}
	var head struct {
		Name hooks.Event `json:"hook_event_name"`
	}
	if err := json.Unmarshal(input, &head); err != nil {
		return Case{}, fmt.Errorf("conformance: case %s: %s: %w", dir, inputName, err)
	}
	if head.Name != event {
		return Case{}, fmt.Errorf("conformance: case %s: hook_event_name %q does not match directory event %q", dir, head.Name, event)
	}
	var wf wantFile
	dec := json.NewDecoder(bytes.NewReader(wantRaw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wf); err != nil {
		return Case{}, fmt.Errorf("conformance: case %s: %s: %w", dir, wantName, err)
	}
	return Case{
		Dir:   dir,
		Event: event,
		Hook: hooks.Hook{
			Name:    string(event) + "/" + path.Base(dir),
			Event:   event,
			Kind:    hooks.KindCommand,
			Timeout: caseTimeout,
			OnError: hooks.OnErrorClosed,
		},
		Input:  json.RawMessage(input),
		Script: script,
		Want:   Want(wf),
	}, nil
}

// Run executes each Case for real through r and reports mismatches via t.
// Cases run one after another, each as a subtest named by its Dir.
//
// For ExitCode 0 the returned Output must equal Want.Output (a nil Want.Output
// means the case must fail). For ExitCode 2 the run must succeed with a deny
// Output. For any other code the run must fail, wrapping an exit status with
// that code. A non-empty ErrSubstr must appear in the error text.
func Run(t *testing.T, cases []Case, r cmdhook.Runner) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.Dir, func(t *testing.T) {
			h := c.Hook
			if h.Command == "" {
				h.Command = writeScript(t, c.Script)
			}
			got, err := r.Run(context.Background(), h, c.Input)
			check(t, c, got, err)
		})
	}
}

func writeScript(t *testing.T, script []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), scriptName)
	//nolint:gosec // G306: the fixture script must be executable to be run.
	if err := os.WriteFile(p, script, 0o755); err != nil {
		t.Fatalf("write fixture script: %v", err)
	}
	return p
}

func check(t *testing.T, c Case, got hooks.Output, err error) {
	t.Helper()
	if c.Want.ErrSubstr != "" {
		if err == nil {
			t.Errorf("want error containing %q, got nil (output %+v)", c.Want.ErrSubstr, got)
			return
		}
		if !strings.Contains(err.Error(), c.Want.ErrSubstr) {
			t.Errorf("error %q does not contain %q", err, c.Want.ErrSubstr)
		}
	}
	switch c.Want.ExitCode {
	case 0:
		if c.Want.Output == nil {
			if err == nil {
				t.Errorf("want failure, got output %+v", got)
			}
			return
		}
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if !reflect.DeepEqual(got, *c.Want.Output) {
			t.Errorf("output = %+v, want %+v", got, *c.Want.Output)
		}
	case cmdhook.BlockExitCode:
		if err != nil {
			t.Errorf("exit 2 is a block, not a failure; got error: %v", err)
			return
		}
		if got.Decision != hooks.DecisionDeny {
			t.Errorf("exit 2 must yield decision deny, got %q", got.Decision)
		}
		if c.Want.Output != nil && !reflect.DeepEqual(got, *c.Want.Output) {
			t.Errorf("output = %+v, want %+v", got, *c.Want.Output)
		}
	default:
		if err == nil {
			t.Errorf("want failure with exit code %d, got output %+v", c.Want.ExitCode, got)
			return
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != c.Want.ExitCode {
			t.Errorf("error %v does not wrap exit code %d", err, c.Want.ExitCode)
		}
	}
}
