package conformance_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	hooks "github.com/hollis-labs/go-hooks"
	"github.com/hollis-labs/go-hooks/cmdhook"
	"github.com/hollis-labs/go-hooks/conformance"
)

func loadAll(t *testing.T) []conformance.Case {
	t.Helper()
	cases, err := conformance.Load(conformance.FixtureFS, conformance.Root)
	if err != nil {
		t.Fatal(err)
	}
	return cases
}

// TestFixturesPassThroughReferenceRunner dogfoods cmdhook.Runner against the
// fixture tree.
func TestFixturesPassThroughReferenceRunner(t *testing.T) {
	conformance.Run(t, loadAll(t), cmdhook.Runner{})
}

// TestFixturesPassInPlace runs the on-disk tree with the committed
// executable bits, the way a non-Go host would.
func TestFixturesPassInPlace(t *testing.T) {
	cases, err := conformance.Load(os.DirFS("."), conformance.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases on disk")
	}
	for i := range cases {
		p, err := filepath.Abs(filepath.Join(cases[i].Dir, "hook.sh"))
		if err != nil {
			t.Fatal(err)
		}
		cases[i].Hook.Command = p
	}
	conformance.Run(t, cases, cmdhook.Runner{})
}

func TestScriptsAreExecutableOnDisk(t *testing.T) {
	for _, c := range loadAll(t) {
		fi, err := os.Stat(filepath.Join(c.Dir, "hook.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&0o111 == 0 {
			t.Errorf("%s/hook.sh is not executable", c.Dir)
		}
	}
}

func TestEveryEventHasACase(t *testing.T) {
	seen := map[hooks.Event]int{}
	for _, c := range loadAll(t) {
		seen[c.Event]++
	}
	for _, e := range hooks.Events() {
		if seen[e] == 0 {
			t.Errorf("no conformance case for %s", e)
		}
	}
}

func TestDecisionEventsCoverAllPaths(t *testing.T) {
	type path struct{ allow, deny, block, failure bool }
	got := map[hooks.Event]*path{
		hooks.EventPreToolUse:        {},
		hooks.EventPermissionRequest: {},
	}
	for _, c := range loadAll(t) {
		p := got[c.Event]
		if p == nil {
			continue
		}
		w := c.Want
		switch {
		case w.ExitCode == 0 && w.Output != nil && w.Output.Decision == hooks.DecisionAllow:
			p.allow = true
		case w.ExitCode == 0 && w.Output != nil && w.Output.Decision == hooks.DecisionDeny:
			p.deny = true
		case w.ExitCode == 2:
			p.block = true
		case w.ExitCode != 0:
			p.failure = true
		}
	}
	for e, p := range got {
		if !p.allow || !p.deny || !p.block || !p.failure {
			t.Errorf("%s coverage incomplete: %+v", e, *p)
		}
	}
}

func TestLoadRejectsBadTrees(t *testing.T) {
	good := func() fstest.MapFS {
		return fstest.MapFS{
			"r/Stop/a/hook.sh":    {Data: []byte("#!/bin/sh\n")},
			"r/Stop/a/input.json": {Data: []byte(`{"hook_event_name":"Stop"}`)},
			"r/Stop/a/want.json":  {Data: []byte(`{"exit_code":0,"output":{},"want_err_substr":""}`)},
		}
	}
	if cases, err := conformance.Load(good(), "r"); err != nil || len(cases) != 1 {
		t.Fatalf("good tree: %v %v", cases, err)
	}
	mut := map[string]func(fstest.MapFS){
		"unknown event dir": func(m fstest.MapFS) { m["r/Interrupt/a/hook.sh"] = &fstest.MapFile{} },
		"missing script":    func(m fstest.MapFS) { delete(m, "r/Stop/a/hook.sh") },
		"event mismatch": func(m fstest.MapFS) {
			m["r/Stop/a/input.json"] = &fstest.MapFile{Data: []byte(`{"hook_event_name":"Stop2"}`)}
		},
		"unknown want field": func(m fstest.MapFS) { m["r/Stop/a/want.json"] = &fstest.MapFile{Data: []byte(`{"exit":0}`)} },
		"bad input json":     func(m fstest.MapFS) { m["r/Stop/a/input.json"] = &fstest.MapFile{Data: []byte(`{`)} },
	}
	for name, f := range mut {
		m := good()
		f(m)
		if _, err := conformance.Load(m, "r"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
