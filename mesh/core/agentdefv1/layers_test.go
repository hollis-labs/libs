package agentdef

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/fstest"
)

func def(name string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(fmt.Sprintf("---\nname: %s\ndescription: d\n---\nbody of %s", name, name))}
}

func TestLoadLayers_Single(t *testing.T) {
	fsys := fstest.MapFS{"agents/a.md": def("a"), "agents/sub/b.md": def("b"), "agents/notes.txt": {Data: []byte("x")}}
	set, err := LoadLayers([]Layer{{FS: fsys, Root: "agents", Name: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.ByName) != 2 || set.ByName["a"].SourceRef != "a.md" || set.ByName["b"].SourceRef != "sub/b.md" || set.ByName["a"].Layer != "one" {
		t.Errorf("unexpected set: %+v", set.ByName)
	}
}

func TestLoadLayers_SkipsSkillsDir(t *testing.T) {
	fsys := fstest.MapFS{
		"a.md":                  def("a"),
		"skills/s/SKILL.md":     {Data: []byte("---\nname: s\ndescription: d\n---\n")},
		"skills/s/reference.md": {Data: []byte("not a definition")},
	}
	set, err := LoadLayers([]Layer{{FS: fsys, Name: "one"}})
	if err != nil || len(set.ByName) != 1 {
		t.Fatalf("set=%v err=%v", set, err)
	}
}

func TestLoadLayers_DistinctNames(t *testing.T) {
	set, err := LoadLayers([]Layer{
		{FS: fstest.MapFS{"a.md": def("a")}, Name: "one"},
		{FS: fstest.MapFS{"b.md": def("b")}, Name: "two"},
	})
	if err != nil || len(set.ByName) != 2 || len(set.Collisions) != 0 {
		t.Fatalf("set=%+v err=%v", set, err)
	}
}

func TestLoadLayers_PrecedenceWins(t *testing.T) {
	hi := fstest.MapFS{"a.md": def("a")}
	lo := fstest.MapFS{"x.md": def("a")}
	for _, layers := range [][]Layer{
		{{FS: lo, Name: "lo", Precedence: 1}, {FS: hi, Name: "hi", Precedence: 2}},
		{{FS: hi, Name: "hi", Precedence: 2}, {FS: lo, Name: "lo", Precedence: 1}},
	} {
		set, err := LoadLayers(layers)
		if err != nil {
			t.Fatalf("declared precedence must resolve silently: %v", err)
		}
		if set.ByName["a"].Layer != "hi" {
			t.Errorf("winner = %q", set.ByName["a"].Layer)
		}
		if len(set.Collisions) != 1 || !set.Collisions[0].Resolved {
			t.Errorf("resolved collision should be recorded for audit: %+v", set.Collisions)
		}
	}
}

func TestLoadLayers_EqualPrecedenceCollides(t *testing.T) {
	set, err := LoadLayers([]Layer{
		{FS: fstest.MapFS{"a.md": def("a")}, Name: "one"},
		{FS: fstest.MapFS{"a.md": def("a")}, Name: "two"},
	})
	if err == nil {
		t.Fatal("equal precedence collision must be an error")
	}
	var ce *CollisionError
	if !errors.As(err, &ce) || ce.Name != "a" || len(ce.Layers) != 2 || ce.Layers[0] != "one" || ce.Layers[1] != "two" || ce.Resolved {
		t.Fatalf("bad collision error: %v", err)
	}
	if len(set.Collisions) != 1 || set.ByName["a"].Layer != "one" {
		t.Errorf("set should retain first and list the collision: %+v", set)
	}
}

func TestLoadLayers_ParseErrorAborts(t *testing.T) {
	set, err := LoadLayers([]Layer{{FS: fstest.MapFS{"bad.md": {Data: []byte("nope")}}, Name: "one"}})
	if err == nil || set != nil {
		t.Fatalf("set=%v err=%v", set, err)
	}
}

func TestLoadLayers_Concurrent(t *testing.T) {
	layers := []Layer{
		{FS: fstest.MapFS{"a.md": def("a"), "b.md": def("b")}, Name: "one", Precedence: 1},
		{FS: fstest.MapFS{"a.md": def("a")}, Name: "two"},
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set, err := LoadLayers(layers)
			if err != nil || len(set.ByName) != 2 {
				t.Errorf("set=%v err=%v", set, err)
			}
			for range set.ByName { // read-only sharing of results
			}
		}()
	}
	wg.Wait()
}
