package goldens_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agentlaunch "github.com/hollis-labs/substrate/harness/agentlaunch"
)

func TestBuildInjectionRejectsUnsafePaths(t *testing.T) {
	_, _, err := baselineBuildInjection(baselineRequest{Overlays: map[string]string{"../escape": "x"}})
	if !errors.Is(err, baselineErrUnsafePath) {
		t.Fatalf("err = %v, want baselineErrUnsafePath", err)
	}
}

func TestTaskBundleRejectsUnsafePaths(t *testing.T) {
	_, err := baselineTaskBundle("tasks", map[string]string{"../bad.md": "x"})
	if err == nil {
		t.Fatal("expected unsafe path error")
	}
}

func TestBuildInjectionNativeFiles(t *testing.T) {
	_, _, err := baselineBuildInjection(baselineRequest{NativeFiles: []agentlaunch.NativeFile{{
		Kind: agentlaunch.NativeFileRaw, RelPath: "tasks/README.md", Content: "read me",
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteInjectionSpecOrderAndOverlayWins(t *testing.T) {
	dir := baselineFixturePrivateDir(t)
	var calls []string
	w := baselineWriter{Authorize: baselineFixtureAuthorization(t), OnWritten: func(file baselineWrittenFile) { calls = append(calls, file.RelPath+":"+file.Source) }}
	spec := agentlaunch.InjectionSpec{
		NativeFiles: []agentlaunch.NativeFile{
			baselineRaw("same.md", "native", 0),
			baselineRaw("native-only.md", "native-only", 0),
		},
		BootDirOverlay: map[string]string{
			"z.md":    "z",
			"a.md":    "a",
			"same.md": "overlay",
		},
	}
	result, err := w.WriteInjectionSpec(dir, spec, baselineWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gotOrder := baselineRels(result.Files)
	wantOrder := []string{"same.md", "native-only.md", "a.md", "same.md", "z.md"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("write order = %v, want %v", gotOrder, wantOrder)
	}
	if string(baselineMustRead(t, filepath.Join(dir, "same.md"))) != "overlay" {
		t.Fatal("overlay did not win over native file at same path")
	}
	if !reflect.DeepEqual(calls, []string{
		"same.md:native",
		"native-only.md:native",
		"a.md:overlay",
		"same.md:overlay",
		"z.md:overlay",
	}) {
		t.Fatalf("atomic calls = %v", calls)
	}
}

func TestPlanInjectionSpecMatchesActualWriteOrder(t *testing.T) {
	spec := agentlaunch.InjectionSpec{
		NativeFiles: []agentlaunch.NativeFile{baselineRaw("n.md", "n", 0)},
		BootDirOverlay: map[string]string{
			"b.md": "b",
			"a.md": "a",
		},
	}
	plan, err := baselinePlanInjectionSpec(spec, baselineWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteInjectionSpec(baselineFixturePrivateDir(t), spec, baselineWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plannedRels(plan.Files), baselineRels(result.Files)) {
		t.Fatalf("plan order = %v, write order = %v", plannedRels(plan.Files), baselineRels(result.Files))
	}
	if got, want := sources(result.Files), []string{baselineSourceNative, baselineSourceOverlay, baselineSourceOverlay}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
}

func TestWriteRejectsUnsafeAndEmptyPaths(t *testing.T) {
	cases := []struct {
		name string
		spec agentlaunch.InjectionSpec
		err  error
	}{
		{
			name: "native unsafe",
			spec: agentlaunch.InjectionSpec{NativeFiles: []agentlaunch.NativeFile{baselineRaw("../bad.md", "x", 0)}},
			err:  baselineErrUnsafePath,
		},
		{
			name: "overlay unsafe",
			spec: agentlaunch.InjectionSpec{BootDirOverlay: map[string]string{"/abs.md": "x"}},
			err:  baselineErrUnsafePath,
		},
		{
			name: "native empty rel",
			spec: agentlaunch.InjectionSpec{NativeFiles: []agentlaunch.NativeFile{baselineRaw("", "x", 0)}},
			err:  baselineErrEmptyRelPath,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteInjectionSpec(baselineFixturePrivateDir(t), tc.spec, baselineWriteOptions{})
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}

	_, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteFiles(baselineFixturePrivateDir(t), []baselineFile{{RelPath: ".git/config", Content: "x"}})
	if !errors.Is(err, baselineErrUnsafePath) {
		t.Fatalf("partial unsafe err = %v, want baselineErrUnsafePath", err)
	}
}

func TestUnsupportedNativeKindAndResolverHook(t *testing.T) {
	spec := agentlaunch.InjectionSpec{NativeFiles: []agentlaunch.NativeFile{{
		Kind: agentlaunch.NativeFileSkill,
		ID:   "review",
	}}}
	_, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteInjectionSpec(baselineFixturePrivateDir(t), spec, baselineWriteOptions{})
	if !errors.Is(err, baselineErrUnsupportedNativeKind) {
		t.Fatalf("err = %v, want baselineErrUnsupportedNativeKind", err)
	}

	dir := baselineFixturePrivateDir(t)
	_, err = (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteInjectionSpec(dir, spec, baselineWriteOptions{
		baselineNativeResolver: func(nf agentlaunch.NativeFile) (baselineFile, error) {
			return baselineFile{RelPath: "skills/" + nf.ID + ".md", Content: "skill"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(baselineMustRead(t, filepath.Join(dir, "skills/review.md"))) != "skill" {
		t.Fatal("resolver output was not written")
	}
}

func TestModesDefaultAndExplicitPreserved(t *testing.T) {
	dir := baselineFixturePrivateDir(t)
	result, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteInjectionSpec(dir, agentlaunch.InjectionSpec{
		NativeFiles: []agentlaunch.NativeFile{
			baselineRaw("default.md", "x", 0),
			baselineRaw("private.md", "x", 0o600),
		},
	}, baselineWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Files[0].Mode; got != 0o644 {
		t.Fatalf("default mode = %#o, want 0644", got)
	}
	if got := result.Files[1].Mode; got != 0o600 {
		t.Fatalf("explicit mode = %#o, want 0600", got)
	}
	if mode := mustMode(t, filepath.Join(dir, "private.md")); mode != 0o600 {
		t.Fatalf("filesystem mode = %#o, want 0600", mode)
	}
}

func TestWriteFilesRewritesOnlySpecifiedFiles(t *testing.T) {
	dir := baselineFixturePrivateDir(t)

	if _, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteFiles(dir, []baselineFile{{RelPath: "slot.md", Content: "v1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sibling.md"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteFiles(dir, []baselineFile{{RelPath: "slot.md", Content: "v2"}}); err != nil {
		t.Fatal(err)
	}
	if string(baselineMustRead(t, filepath.Join(dir, "slot.md"))) != "v2" {
		t.Fatal("slot was not rewritten")
	}
	if string(baselineMustRead(t, filepath.Join(dir, "sibling.md"))) != "keep" {
		t.Fatal("sibling was touched")
	}
}

func TestEmptyBootDirRejected(t *testing.T) {
	_, err := (baselineWriter{Authorize: baselineFixtureAuthorization(t)}).WriteFiles("", []baselineFile{{RelPath: "x.md", Content: "x"}})
	if !errors.Is(err, baselineErrEmptyBootDir) {
		t.Fatalf("err = %v, want baselineErrEmptyBootDir", err)
	}
}

func baselineRaw(rel, content string, mode fs.FileMode) agentlaunch.NativeFile {
	return agentlaunch.NativeFile{Kind: agentlaunch.NativeFileRaw, RelPath: rel, Content: content, Mode: mode}
}

func baselineRels(files []baselineWrittenFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.RelPath)
	}
	return out
}

func plannedRels(files []baselinePlannedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.RelPath)
	}
	return out
}

func sources(files []baselineWrittenFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Source)
	}
	return out
}

func baselineMustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
