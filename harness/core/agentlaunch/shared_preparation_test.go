package agentlaunch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/agentcomposition"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

func TestResolvePreparation_ResolvedCompositionInstallsDocumentsAndArtifacts(t *testing.T) {
	bootRoot := fixturePrivateDir(t)
	comp := agentcomposition.ResolvedComposition{
		ID:        "resolved",
		Documents: []agentcomposition.Document{{ID: "instructions", Path: "AGENTS.md", Sections: []agentcomposition.Section{{ID: "base", Content: "base instructions"}}}},
		Artifacts: artifact.Tree{Entries: []artifact.Entry{{Path: "bin/tool", Kind: artifact.EntryFile, Mode: 0o755, Bytes: []byte{0, 1, 2}}}},
	}
	prepared, err := resolveWithAuthority(t, context.Background(), PrepareRequest{
		Kind:        PrepareInputResolvedComposition,
		Composition: &comp,
		Roots:       ExecutionRoots{BootRoot: bootRoot, CWD: bootRoot, ProjectRoot: fixturePrivateDir(t)},
		Access:      AccessRequirements{Mode: AccessRequired, Host: ExecutionHostLocal},
	})
	if err != nil {
		t.Fatalf("ResolvePreparation: %v", err)
	}
	if prepared.Materialization == nil || !prepared.Materialization.Report.Complete {
		t.Fatalf("materialization incomplete: %#v", prepared.Materialization)
	}
	if got, err := os.ReadFile(filepath.Join(bootRoot, "AGENTS.md")); err != nil || string(got) != "base instructions\n" {
		t.Fatalf("AGENTS.md = %q err=%v", got, err)
	}
	if info, err := os.Stat(filepath.Join(bootRoot, "bin/tool")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("bin/tool mode = %v err=%v", info, err)
	}
	if prepared.Access.Roots.BootRoot != bootRoot || prepared.Roots.ProjectRoot == "" {
		t.Fatalf("access/root separation not preserved: access=%#v roots=%#v", prepared.Access, prepared.Roots)
	}
}

func TestResolvePreparation_AuthoredRecipeAndRawArtifactsUseSameMaterializer(t *testing.T) {
	bootRoot := fixturePrivateDir(t)
	recipe := agentcomposition.AuthoredRecipe{ID: "recipe", Documents: []agentcomposition.Document{{ID: "doc", Path: "doc.md", Content: "hello"}}}
	prepared, err := resolveWithAuthority(t, context.Background(), PrepareRequest{Kind: PrepareInputAuthoredRecipe, Recipe: &recipe, Roots: ExecutionRoots{BootRoot: bootRoot, CWD: bootRoot}})
	if err != nil {
		t.Fatalf("resolveWithAuthority(t, authored): %v", err)
	}
	if prepared.Materialization == nil {
		t.Fatal("authored recipe did not materialize")
	}
	if got, err := os.ReadFile(filepath.Join(bootRoot, "doc.md")); err != nil || string(got) != "hello\n" {
		t.Fatalf("doc.md = %q err=%v", got, err)
	}

	rawRoot := fixturePrivateDir(t)
	raw := artifact.Tree{Entries: []artifact.Entry{{Path: "raw.bin", Kind: artifact.EntryFile, Mode: 0o600, Bytes: []byte{9, 8, 7}}}}
	rawPrepared, err := resolveWithAuthority(t, context.Background(), PrepareRequest{Kind: PrepareInputArtifacts, Artifacts: &raw, Roots: ExecutionRoots{BootRoot: rawRoot, CWD: rawRoot}})
	if err != nil {
		t.Fatalf("resolveWithAuthority(t, raw): %v", err)
	}
	if rawPrepared.Materialization == nil {
		t.Fatal("raw artifacts did not materialize")
	}
	if info, err := os.Stat(filepath.Join(rawRoot, "raw.bin")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("raw.bin mode = %v err=%v", info, err)
	}
}
