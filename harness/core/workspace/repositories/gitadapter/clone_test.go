package gitadapter

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// fixtureCloner is a plain byte copy for disposable test fixtures. It reports
// repositories.FixtureCopy, never a copy-on-write method, so every receipt it
// produces says it was not COW.
type fixtureCloner struct {
	unsupported string
	failAfter   int
	clones      int
}

func (f *fixtureCloner) method() repositories.CloneMethod { return repositories.FixtureCopy }
func (f *fixtureCloner) probe(*os.File, *os.File) (bool, string, error) {
	return f.unsupported == "", f.unsupported, nil
}
func (f *fixtureCloner) clone(src, dir *os.File, name string, perm fs.FileMode) error {
	f.clones++
	if f.failAfter > 0 && f.clones > f.failAfter {
		return errors.New("fixture clone failure")
	}
	root, e := os.OpenRoot(dir.Name())
	if e != nil {
		return e
	}
	defer root.Close()
	dst, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if e != nil {
		return e
	}
	if _, e = io.Copy(dst, src); e != nil {
		dst.Close()
		return e
	}
	if e = dst.Chmod(perm); e != nil {
		dst.Close()
		return e
	}
	return dst.Close()
}

type recordingSink struct{ records []effects.Evidence }

func (s *recordingSink) Record(_ context.Context, e effects.Evidence) error {
	s.records = append(s.records, e.Clone())
	return nil
}

func git(t *testing.T, p *Port, dir string, args ...string) string {
	t.Helper()
	out, _, e := p.run(context.Background(), dir, args...)
	if e != nil {
		t.Fatal("git failed", args, e)
	}
	return trim(out)
}

// cloneFixture extends the worktree fixture with a source holding packed and
// loose objects, nested and executable files, a symlink, and ignored and
// untracked private files that must never reach a clone.
func cloneFixture(t *testing.T, requirement repositories.CloneRequirement) (*Port, repositories.Request, effects.ApplyContext, *fixtureCloner) {
	t.Helper()
	p, r, c := fixture(t)
	src := r.Source.Path
	write := func(name, body string, mode os.FileMode) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(filepath.Join(src, name)), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(src, name), []byte(body), mode); e != nil {
			t.Fatal(e)
		}
	}
	write("dir/nested/file.txt", "nested\n", 0644)
	write("tool.sh", "#!/bin/sh\n", 0755)
	write(".gitignore", ".env\n", 0644)
	if e := os.Symlink("tracked.txt", filepath.Join(src, "link")); e != nil {
		t.Fatal(e)
	}
	git(t, p, src, "add", "--", "dir", "tool.sh", ".gitignore", "link")
	git(t, p, src, "commit", "-m", "tree")
	git(t, p, src, "repack", "-a", "-d")
	write("tracked.txt", "loose\n", 0644)
	git(t, p, src, "commit", "-am", "loose")
	write(".env", "TOKEN=fixture-secret\n", 0600)
	write("untracked.txt", "private\n", 0600)
	r.BaseCommit = git(t, p, src, "rev-parse", "HEAD")
	r.Mode = repositories.Clone
	r.Path = filepath.Join(r.Base.Path, "clone")
	r.Branch = "agent/clone"
	r.Clone = repositories.CloneIntent{Requirement: requirement}
	cow := &fixtureCloner{}
	p.cow = cow
	return p, r, c, cow
}

func TestActualFilesystemPairCapabilityIsHonest(t *testing.T) {
	p, r, c, _ := cloneFixture(t, repositories.CloneRequired)
	p.cow = newCloner()
	method, reason, e := p.CloneCapability(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("actual pair capability: method=%s reason=%s", method, reason)
	if method.CopyOnWrite() {
		t.Skip("copy-on-write available on this test filesystem; the unsupported path is unexercised here")
	}
	if method != repositories.NoClone || reason == "" {
		t.Fatal("unsupported pair must answer none with a reason", method, reason)
	}
	_, out := repositories.Preflight(context.Background(), r, c.PreflightContext, p)
	if out.Outcome != effects.Unsupported || out.Code != "clone_unsupported" {
		t.Fatal(out)
	}
	if _, e = os.Lstat(r.Path); !os.IsNotExist(e) {
		t.Fatal("unsupported required clone mutated", e)
	}
	entries, e := os.ReadDir(r.Base.Path)
	if e != nil || len(entries) != 0 {
		t.Fatal("probe fixture left in attachment base", entries, e)
	}
}

func TestRequiredOrUnauthorizedPreferredRefusesBeforeMutation(t *testing.T) {
	for _, requirement := range []repositories.CloneRequirement{repositories.CloneRequired, repositories.ClonePreferred} {
		p, r, c, cow := cloneFixture(t, requirement)
		cow.unsupported = "fixture_unsupported"
		var commands [][]string
		p.trace = func(args []string) { commands = append(commands, args) }
		_, out := repositories.Preflight(context.Background(), r, c.PreflightContext, p)
		if out.Outcome != effects.Unsupported || out.Code != "clone_unsupported" {
			t.Fatal(requirement, out)
		}
		if _, e := os.Lstat(r.Path); !os.IsNotExist(e) || cow.clones != 0 {
			t.Fatal("refused clone mutated", e, cow.clones)
		}
		for _, args := range commands {
			if args[0] == "init" || args[0] == "worktree" && len(args) > 1 && args[1] == "add" {
				t.Fatal("mutating command before refusal", args)
			}
		}
	}
}

func TestPreferredFallbackRecordsAuthorizedWorktree(t *testing.T) {
	p, r, c, cow := cloneFixture(t, repositories.ClonePreferred)
	cow.unsupported = "fixture_unsupported"
	r.Clone.FallbackAuthorizationID, r.Clone.FallbackAuthorizationVersion = "fallback-grant", "fallback-revision"
	prepared, pre := repositories.Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	out := repositories.Apply(context.Background(), prepared, c, p)
	a := out.Evidence.Attachments[0]
	if out.Outcome != effects.Applied || a.Mode != string(repositories.Worktree) || a.RequestedMode != string(repositories.Clone) || a.Method != string(repositories.NoClone) || a.FallbackReason != "fixture_unsupported" || a.FallbackAuthorizationID != "fallback-grant" || a.FallbackAuthorizationVersion != "fallback-revision" || a.BaseCommit != r.BaseCommit || a.Branch != r.Branch {
		t.Fatal("fallback receipt", out)
	}
	if cow.clones != 0 {
		t.Fatal("fallback cloned files")
	}
	if got := git(t, p, r.Path, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != r.Common.Path {
		t.Fatal("fallback is not a registered worktree", got)
	}
	if resumed := repositories.InspectResume(context.Background(), r, c.PreflightContext, p, out.Evidence); resumed.Outcome != effects.AlreadyPresent {
		t.Fatal("frozen clone request does not resume fallback", resumed)
	}
	forged := out.Evidence.Clone()
	forged.Attachments[0].FallbackAuthorizationID = "other"
	if resumed := repositories.InspectResume(context.Background(), r, c.PreflightContext, p, forged); resumed.Outcome != effects.Refused {
		t.Fatal("fallback receipt with other authorization accepted", resumed)
	}
	m := repositories.MergeBack{Header: effects.Header{Version: effects.SchemaVersion, OperationID: "merge", InputDigest: "merge-input"}, Attachment: r, Receipt: out.Evidence, TargetBranch: "agent/merged", AuthorizationID: "merge-grant", AuthorizationVersion: "merge-revision"}
	mc := c.PreflightContext
	mc.Header = m.Header
	if _, res := repositories.CheckMergeBack(context.Background(), m, mc, p); res.Outcome != effects.Refused {
		t.Fatal("merge-back accepted a worktree fallback", res)
	}
}

func applyClone(t *testing.T, p *Port, r repositories.Request, c effects.ApplyContext) effects.Result {
	t.Helper()
	prepared, pre := repositories.Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	return repositories.Apply(context.Background(), prepared, c, p)
}

func TestFixtureCloneIsIndependentPrivateRepository(t *testing.T) {
	p, r, c, cow := cloneFixture(t, repositories.CloneRequired)
	sourceStatus := git(t, p, r.Source.Path, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching")
	sourceWorktrees := git(t, p, r.Source.Path, "worktree", "list", "--porcelain")
	sink := &recordingSink{}
	c.Receipts = sink
	out := applyClone(t, p, r, c)
	a := out.Evidence.Attachments[0]
	if out.Outcome != effects.Applied || a.Mode != string(repositories.Clone) || a.RequestedMode != string(repositories.Clone) || a.Method != string(repositories.FixtureCopy) || a.FallbackReason != "" || a.FallbackAuthorizationID != "" || !a.Created || a.Head != r.BaseCommit || a.BaseCommit != r.BaseCommit || a.Branch != r.Branch || a.RepositoryID != r.RepositoryID || a.Path != r.Path {
		t.Fatal("clone receipt", out)
	}
	if repositories.CloneMethod(a.Method).CopyOnWrite() {
		t.Fatal("fixture construction recorded as copy on write")
	}
	if len(sink.records) != 2 || sink.records[0].Phase != effects.IntentPhase || sink.records[1].Phase != effects.CompletePhase {
		t.Fatal("receipt phases", sink.records)
	}
	if cow.clones == 0 {
		t.Fatal("no file was constructed through the cloner")
	}
	gitDir := filepath.Join(r.Path, ".git")
	if st, e := os.Lstat(gitDir); e != nil || !st.IsDir() {
		t.Fatal("clone metadata is not a private directory", e)
	}
	if got := git(t, p, r.Path, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != gitDir {
		t.Fatal("clone shares common metadata", got)
	}
	if got := git(t, p, r.Path, "symbolic-ref", "HEAD"); got != "refs/heads/"+r.Branch {
		t.Fatal("clone branch", got)
	}
	if _, e := os.Lstat(filepath.Join(gitDir, "objects/info/alternates")); !os.IsNotExist(e) {
		t.Fatal("clone uses alternates", e)
	}
	if _, e := os.Lstat(filepath.Join(gitDir, "hooks")); !os.IsNotExist(e) {
		t.Fatal("template hooks installed", e)
	}
	if _, code, _ := p.run(context.Background(), r.Path, "config", "--local", "--get", "user.name"); code != 1 {
		t.Fatal("source configuration copied into clone")
	}
	si, e := os.Stat(filepath.Join(r.Common.Path, "index"))
	ci, e2 := os.Stat(filepath.Join(gitDir, "index"))
	if e != nil || e2 != nil || os.SameFile(si, ci) {
		t.Fatal("index shared", e, e2)
	}
	packs := 0
	e = filepath.WalkDir(filepath.Join(gitDir, "objects"), func(path string, d fs.DirEntry, e error) error {
		if e != nil || d.IsDir() {
			return e
		}
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if st.Sys().(*syscall.Stat_t).Nlink != 1 {
			t.Error("object file shares an inode", path)
		}
		if strings.HasSuffix(path, ".pack") {
			packs++
		}
		return nil
	})
	if e != nil || packs == 0 {
		t.Fatal("pack objects not cloned", e, packs)
	}
	for _, name := range []string{".env", "untracked.txt"} {
		if _, e := os.Lstat(filepath.Join(r.Path, name)); !os.IsNotExist(e) {
			t.Fatal("untracked or ignored source file reached clone", name, e)
		}
	}
	if st, e := os.Stat(filepath.Join(r.Path, "tool.sh")); e != nil || st.Mode().Perm()&0100 == 0 {
		t.Fatal("executable mode lost", e)
	}
	if target, e := os.Readlink(filepath.Join(r.Path, "link")); e != nil || target != "tracked.txt" {
		t.Fatal("symlink", target, e)
	}
	if body, e := os.ReadFile(filepath.Join(r.Path, "dir/nested/file.txt")); e != nil || string(body) != "nested\n" {
		t.Fatal("nested file", e)
	}
	if git(t, p, r.Source.Path, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching") != sourceStatus || git(t, p, r.Source.Path, "worktree", "list", "--porcelain") != sourceWorktrees {
		t.Fatal("source changed by clone attachment")
	}
	if _, code, _ := p.run(context.Background(), r.Source.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+r.Branch); code != 1 {
		t.Fatal("clone branch created in source")
	}
	if e := os.WriteFile(filepath.Join(r.Path, "tracked.txt"), []byte("agent work\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if resumed := repositories.InspectResume(context.Background(), r, c.PreflightContext, p, out.Evidence); resumed.Outcome != effects.AlreadyPresent {
		t.Fatal("dirty clone does not resume", resumed)
	}
	if body, _ := os.ReadFile(filepath.Join(r.Path, "tracked.txt")); string(body) != "agent work\n" {
		t.Fatal("resume changed agent work")
	}
	forged := out.Evidence.Clone()
	forged.Attachments[0].FallbackReason = "fabricated"
	if resumed := repositories.InspectResume(context.Background(), r, c.PreflightContext, p, forged); resumed.Outcome != effects.Refused {
		t.Fatal("clone receipt with a fallback reason accepted", resumed)
	}
	forged = out.Evidence.Clone()
	forged.Attachments[0].Method = string(repositories.NoClone)
	if resumed := repositories.InspectResume(context.Background(), r, c.PreflightContext, p, forged); resumed.Outcome != effects.Refused {
		t.Fatal("clone receipt without a construction method accepted", resumed)
	}
	retire := repositories.Retirement{Header: effects.Header{Version: effects.SchemaVersion, OperationID: "retire", InputDigest: "retire-input"}, Attachment: r, Receipt: out.Evidence, AcceptedHead: a.Head, ShippedProofID: "s", ShippedProofRevision: "s1", UnusedProofID: "u", UnusedProofRevision: "u1", Provenance: "test", AuthorizationID: "remove", AuthorizationVersion: "1"}
	rc := c.PreflightContext
	rc.Header = retire.Header
	if _, res := repositories.CheckRetirement(context.Background(), retire, rc, p); res.Outcome != effects.Unsupported || res.Code != "clone_retirement_unsupported" {
		t.Fatal("clone retirement", res)
	}
	if _, e := os.Lstat(r.Path); e != nil {
		t.Fatal("clone not retained", e)
	}
}

func TestDirtyOrMovedSourceIsNotCloned(t *testing.T) {
	for _, kind := range []string{"dirty", "moved", "index-flag"} {
		t.Run(kind, func(t *testing.T) {
			p, r, c, cow := cloneFixture(t, repositories.CloneRequired)
			want := map[string]string{"dirty": "source_dirty", "moved": "source_not_at_base", "index-flag": "source_index_flags"}[kind]
			switch kind {
			case "dirty":
				if e := os.WriteFile(filepath.Join(r.Source.Path, "tracked.txt"), []byte("live edit\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "moved":
				r.BaseCommit = git(t, p, r.Source.Path, "rev-parse", "HEAD~1")
			case "index-flag":
				git(t, p, r.Source.Path, "update-index", "--skip-worktree", "tracked.txt")
			}
			method, reason, e := p.CloneCapability(context.Background(), r)
			if e != nil || method != repositories.NoClone || reason != want {
				t.Fatal(method, reason, e)
			}
			if _, out := repositories.Preflight(context.Background(), r, c.PreflightContext, p); out.Outcome != effects.Unsupported {
				t.Fatal(out)
			}
			if _, e = os.Lstat(r.Path); !os.IsNotExist(e) || cow.clones != 0 {
				t.Fatal("source state manufactured a clone", e)
			}
		})
	}
}

func TestPartialCloneFailureRetainsWithoutCopyFallback(t *testing.T) {
	p, r, c, cow := cloneFixture(t, repositories.CloneRequired)
	cow.failAfter = 2
	var commands [][]string
	p.trace = func(args []string) { commands = append(commands, args) }
	out := applyClone(t, p, r, c)
	if out.Outcome != effects.Partial || out.Code != "create_uncertain" {
		t.Fatal(out)
	}
	recovery := false
	for _, o := range out.Obligations {
		recovery = recovery || o.Code == "recovery_required"
	}
	if !recovery {
		t.Fatal("partial clone has no recovery obligation", out.Obligations)
	}
	if _, e := os.Lstat(r.Path); e != nil {
		t.Fatal("partial clone not retained", e)
	}
	for _, args := range commands {
		if args[0] == "worktree" || args[0] == "checkout" || args[0] == "clone" {
			t.Fatal("fallback after clone mutation", args)
		}
	}
}

func TestCapabilityChangeBetweenPreflightAndApplyRefuses(t *testing.T) {
	p, r, c, cow := cloneFixture(t, repositories.ClonePreferred)
	r.Clone.FallbackAuthorizationID, r.Clone.FallbackAuthorizationVersion = "fallback-grant", "fallback-revision"
	prepared, pre := repositories.Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared || pre.Evidence.Attachments[0].Mode != string(repositories.Clone) {
		t.Fatal(pre)
	}
	cow.unsupported = "fixture_unsupported"
	out := repositories.Apply(context.Background(), prepared, c, p)
	if out.Outcome != effects.Refused || out.Code != "clone_capability_changed" {
		t.Fatal(out)
	}
	if _, e := os.Lstat(r.Path); !os.IsNotExist(e) {
		t.Fatal("mode switched after preflight", e)
	}
}

func TestMergeBackIsExplicitFastForwardWithExactTarget(t *testing.T) {
	p, r, c, _ := cloneFixture(t, repositories.CloneRequired)
	out := applyClone(t, p, r, c)
	if out.Outcome != effects.Applied {
		t.Fatal(out)
	}
	git(t, p, r.Path, "-c", "user.name=Agent", "-c", "user.email=agent@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "agent work")
	head := git(t, p, r.Path, "rev-parse", "HEAD")
	sourceHead := git(t, p, r.Source.Path, "rev-parse", "HEAD")
	h := effects.Header{Version: effects.SchemaVersion, OperationID: "merge", InputDigest: "merge-input"}
	mc := c
	mc.Header = h
	sink := &recordingSink{}
	mc.Receipts = sink
	m := repositories.MergeBack{Header: h, Attachment: r, Receipt: out.Evidence, TargetBranch: "agent/merged", AuthorizationID: "merge-grant", AuthorizationVersion: "merge-revision"}

	unauthorized := m
	unauthorized.AuthorizationID = ""
	if _, res := repositories.CheckMergeBack(context.Background(), unauthorized, mc.PreflightContext, p); res.Outcome != effects.Refused || res.Code != "mergeback_binding_refused" {
		t.Fatal("merge-back without its own authorization", res)
	}
	checkedOut := m
	checkedOut.TargetBranch, checkedOut.ExpectedTarget = "main", sourceHead
	if _, res := repositories.CheckMergeBack(context.Background(), checkedOut, mc.PreflightContext, p); res.Outcome != effects.Refused || res.Code != "target_unavailable" {
		t.Fatal("merge-back into a checked-out branch", res)
	}
	stale := m
	stale.ExpectedTarget = sourceHead
	if _, res := repositories.CheckMergeBack(context.Background(), stale, mc.PreflightContext, p); res.Outcome != effects.Refused || res.Code != "target_changed" {
		t.Fatal("merge-back with wrong expected target", res)
	}

	ticket, res := repositories.CheckMergeBack(context.Background(), m, mc.PreflightContext, p)
	if res.Outcome != effects.Prepared {
		t.Fatal(res)
	}
	applied := repositories.ApplyMergeBack(context.Background(), ticket, mc, p)
	a := applied.Evidence.Attachments[0]
	if applied.Outcome != effects.Applied || applied.Evidence.Kind != effects.RepositoryMergeBack || a.Head != head || a.TargetBranch != "agent/merged" || a.TargetBefore != "" || a.AuthorizationID != "merge-grant" || a.OriginHeader != r.Header || a.Method != string(repositories.FixtureCopy) {
		t.Fatal("merge-back receipt", applied)
	}
	if len(sink.records) != 2 || sink.records[0].Phase != effects.IntentPhase {
		t.Fatal("merge-back receipt phases", sink.records)
	}
	if got := git(t, p, r.Source.Path, "rev-parse", "refs/heads/agent/merged"); got != head {
		t.Fatal("source ref", got)
	}
	if git(t, p, r.Source.Path, "rev-parse", "HEAD") != sourceHead || git(t, p, r.Source.Path, "status", "--porcelain=v1", "--untracked-files=no") != "" {
		t.Fatal("merge-back touched the source worktree")
	}

	// A diverged target is never overwritten: the exact old value is required
	// and the clone head must descend from it.
	git(t, p, r.Source.Path, "update-ref", "refs/heads/agent/diverged", git(t, p, r.Source.Path, "commit-tree", "-m", "other", "HEAD^{tree}"))
	diverged := m
	diverged.TargetBranch, diverged.ExpectedTarget = "agent/diverged", git(t, p, r.Source.Path, "rev-parse", "refs/heads/agent/diverged")
	ticket, res = repositories.CheckMergeBack(context.Background(), diverged, mc.PreflightContext, p)
	if res.Outcome != effects.Prepared {
		t.Fatal(res)
	}
	refused := repositories.ApplyMergeBack(context.Background(), ticket, mc, p)
	if refused.Outcome != effects.Refused || refused.Code != "mergeback_refused" {
		t.Fatal("non-fast-forward merge-back", refused)
	}
	if got := git(t, p, r.Source.Path, "rev-parse", "refs/heads/agent/diverged"); got != diverged.ExpectedTarget {
		t.Fatal("diverged target moved", got)
	}
}

func TestCloneAndMergeBackCommandsStayLocalAndUnforced(t *testing.T) {
	p, r, c, _ := cloneFixture(t, repositories.CloneRequired)
	var commands [][]string
	p.trace = func(args []string) { commands = append(commands, args) }
	out := applyClone(t, p, r, c)
	if out.Outcome != effects.Applied {
		t.Fatal(out)
	}
	for _, args := range commands {
		for _, arg := range args {
			switch arg {
			case "fetch", "clone", "pull", "push", "prune", "reset", "clean", "gc", "--force", "-f", "--detach", "--shared", "--reference", "--separate-git-dir":
				t.Fatal("forbidden clone construction command", args)
			}
		}
		if args[0] == "worktree" && len(args) > 1 && args[1] == "add" || args[0] == "checkout-index" && strings.Contains(strings.Join(args, " "), " -a") {
			t.Fatal("clone fell back to a checkout command", args)
		}
	}
	git(t, p, r.Path, "-c", "user.name=Agent", "-c", "user.email=agent@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "agent work")
	h := effects.Header{Version: effects.SchemaVersion, OperationID: "merge", InputDigest: "merge-input"}
	mc := c
	mc.Header = h
	m := repositories.MergeBack{Header: h, Attachment: r, Receipt: out.Evidence, TargetBranch: "agent/merged", AuthorizationID: "merge-grant", AuthorizationVersion: "merge-revision"}
	ticket, res := repositories.CheckMergeBack(context.Background(), m, mc.PreflightContext, p)
	if res.Outcome != effects.Prepared {
		t.Fatal(res)
	}
	commands = nil
	if applied := repositories.ApplyMergeBack(context.Background(), ticket, mc, p); applied.Outcome != effects.Applied {
		t.Fatal(applied)
	}
	fetches := 0
	for _, args := range commands {
		joined := strings.Join(args, "\x00")
		for _, arg := range args {
			switch arg {
			case "push", "pull", "clone", "reset", "clean", "prune", "--force", "-f", "checkout", "switch", "merge":
				t.Fatal("forbidden merge-back command", args)
			}
			if strings.HasPrefix(arg, "+") {
				t.Fatal("forced refspec", args)
			}
		}
		if strings.Contains(joined, "\x00fetch\x00") {
			fetches++
			if args[len(args)-2] != r.Path || args[len(args)-1] != "refs/heads/"+r.Branch || !strings.Contains(joined, "--no-write-fetch-head") || !strings.Contains(joined, "transfer.fsckObjects=true") {
				t.Fatal("unexpected fetch", args)
			}
		}
	}
	if fetches != 1 {
		t.Fatal("merge-back fetch count", fetches)
	}
}
