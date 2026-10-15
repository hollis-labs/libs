package materialize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

func TestInstalledExistingUserDirectoryIsTraversalNotOwnership(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	req := Request{Operation: Operation("install"), TargetRoot: root, Artifacts: artifact.Tree{Entries: []artifact.Entry{
		{Path: ".claude", Kind: artifact.EntryDirectory, Mode: 0755},
		{Path: ".claude/fixture.txt", Kind: artifact.EntryFile, Bytes: []byte("fixture"), Mode: 0600},
	}}}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	req.Generation = "fixture-generation"
	req.Installed = &InstalledPolicy{TargetIdentity: InstalledIdentity(info), ControlIdentity: "external-control", CaseMode: CaseSensitive, Files: []InstalledFileChange{{Path: ".claude/fixture.txt", After: artifact.DigestBytes([]byte("fixture")), AfterMode: 0600, GrantID: "fixture-grant", GrantVersion: "1", Phase: InstalledPrepared}}}
	bindInstalledFixture(t, &req)
	if _, err := NewEngine(EngineOptions{}).Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(filepath.Join(root, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatal("changed existing operator directory mode")
	}
	if _, err := os.Stat(ManifestPath(root)); !os.IsNotExist(err) {
		t.Fatal("published ownership manifest inside user root")
	}
}

func installedFixtureRequest(t *testing.T, root string) Request {
	t.Helper()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Operation: OperationInstall, TargetRoot: root, Generation: "fixture-generation", Artifacts: artifact.Tree{Entries: []artifact.Entry{{Path: ".claude/fixture.txt", Kind: artifact.EntryFile, Mode: 0600, Bytes: []byte("fixture")}}}, Installed: &InstalledPolicy{TargetIdentity: InstalledIdentity(info), ControlIdentity: "external-control", CaseMode: CaseSensitive, Files: []InstalledFileChange{{Path: ".claude/fixture.txt", After: artifact.DigestBytes([]byte("fixture")), AfterMode: 0600, GrantID: "fixture-grant", GrantVersion: "1", Phase: InstalledPrepared}}}}
	bindInstalledFixture(t, &req)
	return req
}
func TestInstalledStageRevalidatedAfterCallback(t *testing.T) {
	root := t.TempDir()
	req := installedFixtureRequest(t, root)
	engine := NewEngine(EngineOptions{BeforeInstalledCommit: func(context.Context, InstalledFileChange) error {
		dirs, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			if strings.HasPrefix(d.Name(), ".installed-stage-") {
				return os.WriteFile(filepath.Join(root, d.Name(), "file-0"), []byte("foreign"), 0600)
			}
		}
		return errors.New("missing stage")
	}})
	h, err := engine.Apply(context.Background(), req)
	if err == nil {
		t.Fatal("accepted changed staged file")
	}
	if !h.Mutated || len(h.Retained) == 0 {
		t.Fatal("lost uncertain stage")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude/fixture.txt")); !os.IsNotExist(err) {
		t.Fatal("published changed staged bytes")
	}
}
func TestInstalledRootRevalidatedAfterCallback(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "target")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	req := installedFixtureRequest(t, root)
	old := filepath.Join(base, "retained")
	engine := NewEngine(EngineOptions{BeforeInstalledCommit: func(context.Context, InstalledFileChange) error {
		if err := os.Rename(root, old); err != nil {
			return err
		}
		return os.Mkdir(root, 0700)
	}})
	h, err := engine.Apply(context.Background(), req)
	if err == nil {
		t.Fatal("accepted replaced target root")
	}
	if !h.Mutated || len(h.Retained) == 0 {
		t.Fatal("lost stage obligation")
	}
	for _, r := range []string{root, old} {
		if _, err := os.Stat(filepath.Join(r, ".claude/fixture.txt")); !os.IsNotExist(err) {
			t.Fatal("published after target replacement")
		}
	}
}
func TestInstalledDirectoryRevalidatedAfterCallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	req := installedFixtureRequest(t, root)
	engine := NewEngine(EngineOptions{BeforeInstalledCommit: func(context.Context, InstalledFileChange) error { return os.Chmod(dir, 0750) }})
	_, err := engine.Apply(context.Background(), req)
	if err == nil {
		t.Fatal("accepted changed traversal directory")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude/fixture.txt")); !os.IsNotExist(err) {
		t.Fatal("published after directory observation changed")
	}
}

func TestInstalledCreatedParentsRecordedWithoutOwningExistingDirs(t *testing.T) {
	root := t.TempDir()
	req := installedFixtureRequest(t, root)
	h, err := NewEngine(EngineOptions{}).Apply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.InstalledDirectories) != 1 || !h.InstalledDirectories[0].Created || h.InstalledDirectories[0].Identity == "" || h.InstalledDirectories[0].Path != ".claude" || h.InstalledDirectories[0].Phase != InstalledVerified {
		t.Fatal("missing exact created parent metadata")
	}
	if h.InstalledStage.Identity == "" || h.InstalledStage.Phase != InstalledVerified {
		t.Fatal("lost exact temporary cleanup custody")
	}
	info, err := os.Stat(filepath.Join(root, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	req = installedFixtureRequest(t, root)
	req.Installed.Files[0].BeforeExists = true
	finfo, err := os.Stat(filepath.Join(root, ".claude/fixture.txt"))
	if err != nil {
		t.Fatal(err)
	}
	req.Installed.Files[0].BeforeIdentity = InstalledIdentity(finfo)
	req.Installed.Files[0].BeforeMode = 0600
	req.Installed.Files[0].Before = req.Installed.Files[0].After
	bindInstalledFixture(t, &req)
	h, err = NewEngine(EngineOptions{}).Apply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if h.InstalledDirectories[0].Created || h.InstalledDirectories[0].Identity != InstalledIdentity(info) {
		t.Fatal("adopted preexisting parent")
	}
}
func TestInstalledAliasesComparisonNeverRewritesPaths(t *testing.T) {
	for _, mode := range []CaseMode{CaseSensitive, CaseInsensitive} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			entries := []artifact.Entry{{Path: ".agents/skills/caf\u00e9/SKILL.md", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture")}, {Path: ".agents/skills/cafe\u0301/SKILL.md", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture")}}
			if _, err := InspectInstalled(context.Background(), root, artifact.Tree{Entries: entries}, mode); !errors.Is(err, ErrConflict) {
				t.Fatal("accepted proposed normalization aliases")
			}
			if err := os.MkdirAll(filepath.Join(root, ".agents/skills/cafe\u0301"), 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectInstalled(context.Background(), root, artifact.Tree{Entries: entries[:1]}, mode); !errors.Is(err, ErrConflict) {
				t.Fatal("accepted observed normalization alias")
			}
			names, err := os.ReadDir(filepath.Join(root, ".agents/skills"))
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != 1 || names[0].Name() != "cafe\u0301" {
				t.Fatal("rewrote observed directory spelling")
			}
		})
	}
	root := t.TempDir()
	entries := []artifact.Entry{{Path: ".agents/skills/EXAMPLE/SKILL.md", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture")}, {Path: ".agents/skills/example/SKILL.md", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture")}}
	if _, err := InspectInstalled(context.Background(), root, artifact.Tree{Entries: entries}, CaseInsensitive); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted platform case aliases")
	}
	if err := checkInstalledAliasFixture(t, root, entries, CaseSensitive); err != nil {
		t.Fatal("refused distinct case-sensitive names")
	}
	if _, err := InspectInstalled(context.Background(), root, artifact.Tree{Entries: entries}, CaseMode("unknown")); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatal("guessed case behavior")
	}
}
func TestInstalledSymlinkAndSpecialTypesRefuseBeforeStage(t *testing.T) {
	for _, kind := range []string{"leaf-symlink", "parent-symlink", "directory-at-leaf"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			other := t.TempDir()
			req := installedFixtureRequest(t, root)
			if kind == "parent-symlink" {
				if err := os.Symlink(other, filepath.Join(root, ".claude")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(root, ".claude"), 0755); err != nil {
					t.Fatal(err)
				}
				if kind == "leaf-symlink" {
					if err := os.Symlink(filepath.Join(other, "foreign"), filepath.Join(root, ".claude/fixture.txt")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(filepath.Join(root, ".claude/fixture.txt"), 0755); err != nil {
						t.Fatal(err)
					}
				}
			}
			h, err := NewEngine(EngineOptions{}).Apply(context.Background(), req)
			if err == nil || h.Mutated {
				t.Fatal("unsafe type reached mutation")
			}
			dirs, _ := os.ReadDir(root)
			for _, d := range dirs {
				if strings.HasPrefix(d.Name(), ".installed-stage-") {
					t.Fatal("unsafe type staged")
				}
			}
		})
	}
}
func TestInstalledCancellationAndCallbackFailureRetainOnlyOwnStage(t *testing.T) {
	for _, kind := range []string{"before", "callback", "foreign-stage"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			req := installedFixtureRequest(t, root)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "before" {
				cancel()
			}
			engine := NewEngine(EngineOptions{BeforeInstalledCommit: func(context.Context, InstalledFileChange) error {
				if kind == "foreign-stage" {
					dirs, _ := os.ReadDir(root)
					for _, d := range dirs {
						if strings.HasPrefix(d.Name(), ".installed-stage-") {
							return os.WriteFile(filepath.Join(root, d.Name(), "foreign"), []byte("retain"), 0600)
						}
					}
				}
				cancel()
				return ctx.Err()
			}})
			h, err := engine.Apply(ctx, req)
			if err == nil {
				t.Fatal("failure completed")
			}
			if kind == "before" && h.Mutated {
				t.Fatal("cancelled before staging mutated")
			}
			if kind != "before" && (!h.Mutated || len(h.Retained) == 0) {
				t.Fatal("lost staged obligation")
			}
			if _, err := os.Stat(filepath.Join(root, ".claude/fixture.txt")); kind != "foreign-stage" && !os.IsNotExist(err) {
				t.Fatal("published after cancellation")
			}
			if kind == "foreign-stage" {
				if _, err := os.Stat(filepath.Join(root, h.InstalledStage.Path, "foreign")); err != nil {
					t.Fatal("removed uncertain content")
				}
			}
		})
	}
}

func bindInstalledFixture(t *testing.T, req *Request) {
	t.Helper()
	cap, err := InspectInstalledCapabilities(context.Background(), req.TargetRoot, req.Installed.CaseMode)
	if errors.Is(err, ErrUnsupportedOperation) {
		t.Skip("installed execution unsupported by this platform/filesystem metadata backend")
	}
	if err != nil {
		t.Fatal(err)
	}
	req.Installed.Capabilities = cap
	snapshots, err := InspectInstalled(context.Background(), req.TargetRoot, req.Artifacts, req.Installed.CaseMode)
	if err != nil {
		return
	} // Unsafe fixtures intentionally exercise engine preflight.
	for j := range req.Installed.Files {
		c := &req.Installed.Files[j]
		for _, s := range snapshots {
			if s.Path == c.Path {
				c.BeforeMetadata = s.Metadata
				c.AfterMetadata = expectedInstalledMetadata(cap.Creation, c.AfterMode)
			}
		}
	}
}

func checkInstalledAliasFixture(t *testing.T, target string, entries []artifact.Entry, mode CaseMode) error {
	t.Helper()
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return checkInstalledAliases(context.Background(), root, entries, mode)
}
