package gitadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

type testSink struct{}

func (testSink) Record(context.Context, effects.Evidence) error { return nil }
func fixture(t *testing.T) (*Port, repositories.Request, effects.ApplyContext) {
	t.Helper()
	exe, e := exec.LookPath("git")
	if e != nil {
		t.Skip("git unavailable")
	}
	exe, e = filepath.Abs(exe)
	if e != nil {
		t.Fatal(e)
	}
	p := New(exe)
	if p.platform != "linux" && p.platform != "darwin" {
		t.Skip("unsupported filesystem platform")
	}
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(base, 0700); e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(base, "source")
	attachments := filepath.Join(base, "attachments")
	for _, path := range []string{source, attachments} {
		if e = os.Mkdir(path, 0700); e != nil {
			t.Fatal(e)
		}
	}
	run := func(args ...string) string {
		t.Helper()
		out, _, e := p.run(context.Background(), source, args...)
		if e != nil {
			t.Fatal("fixture git failed", args, e)
		}
		return trim(out)
	}
	run("init", "-b", "main")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "commit.gpgsign", "false")
	if e = os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("initial\n"), 0600); e != nil {
		t.Fatal(e)
	}
	run("add", "tracked.txt")
	run("commit", "-m", "initial")
	common := filepath.Join(source, ".git")
	if e = os.Chmod(common, 0700); e != nil {
		t.Fatal(e)
	}
	sourceID, e := Identity(source)
	if e != nil {
		t.Fatal(e)
	}
	commonID, e := Identity(common)
	if e != nil {
		t.Fatal(e)
	}
	root := func(id, path string) effects.RootInput {
		return effects.RootInput{ID: id, Path: path, AllowedBase: base, Owner: "owner", Provenance: "fixture", MutationIdentity: path, PrivateCustody: true}
	}
	h := effects.Header{Version: effects.SchemaVersion, OperationID: "operation", InputDigest: "input"}
	r := repositories.Request{Header: h, Mode: repositories.Worktree, Ownership: repositories.Owned, Source: root("source", source), Common: root("common", common), Base: root("base", attachments), Path: filepath.Join(attachments, "work"), RepositoryID: "repo", SourceIdentity: sourceID, CommonIdentity: commonID, Branch: "work/fixture", BaseCommit: run("rev-parse", "HEAD"), AuthorizationID: "grant", AuthorizationVersion: "revision", CandidateRootID: "candidate"}
	locks := filepath.Join(base, "locks")
	c := effects.ApplyContext{PreflightContext: effects.PreflightContext{Header: h, Validate: func(context.Context) error { return nil }, HeldLocks: []effects.LockIdentity{{Namespace: locks, CanonicalID: source}, {Namespace: locks, CanonicalID: common}, {Namespace: locks, CanonicalID: attachments}}}, ArtifactRootID: "candidate", ArtifactGeneration: "generation", Receipts: testSink{}}
	return p, r, c
}
func create(t *testing.T, p *Port, r repositories.Request, c effects.ApplyContext) effects.Result {
	t.Helper()
	prepared, pre := repositories.Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	out := repositories.Apply(context.Background(), prepared, c, p)
	if out.Outcome != effects.Applied {
		t.Fatal(out)
	}
	return out
}
func TestLocalWorktreeCreationDirtyResumeAndRetirement(t *testing.T) {
	p, r, c := fixture(t)
	out := create(t, p, r, c)
	if e := os.WriteFile(filepath.Join(r.Path, "tracked.txt"), []byte("legitimate work\n"), 0600); e != nil {
		t.Fatal(e)
	}
	observed, e := p.Observe(context.Background(), r)
	if e != nil || !observed.Dirty {
		t.Fatal("dirty observation missing", observed, e)
	}
	resumed := repositories.InspectResume(context.Background(), r, c.PreflightContext, p, out.Evidence)
	if resumed.Outcome != effects.AlreadyPresent {
		t.Fatal(resumed)
	}
	safety, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0])
	if e != nil || !safety.Complete || !safety.Dirty {
		t.Fatal(safety, e)
	}
	if removed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); e == nil || removed {
		t.Fatal("dirty work removed", removed, e)
	}
	actual, e := os.ReadFile(filepath.Join(r.Path, "tracked.txt"))
	if e != nil || string(actual) != "legitimate work\n" {
		t.Fatal("dirty work lost", e)
	}
	if e = os.WriteFile(filepath.Join(r.Path, "tracked.txt"), []byte("initial\n"), 0600); e != nil {
		t.Fatal(e)
	}
	safety, e = p.Safety(context.Background(), r, out.Evidence.Attachments[0])
	if e != nil || !safety.Complete || safety.Dirty || safety.Unknown || safety.Untracked || safety.Ignored || safety.Locked {
		t.Fatal(safety, e)
	}
	removed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate)
	if e != nil || !removed {
		t.Fatal(removed, e)
	}
	if _, e = os.Lstat(r.Path); !os.IsNotExist(e) {
		t.Fatal("attachment retained", e)
	}
	if _, _, e = p.run(context.Background(), r.Source.Path, "show-ref", "--verify", "refs/heads/"+r.Branch); e != nil {
		t.Fatal("branch deleted", e)
	}
}
func TestIgnoredUnknownAndLockedDataRetained(t *testing.T) {
	for _, kind := range []string{"ignored", "untracked", "unknown-admin", "locked"} {
		t.Run(kind, func(t *testing.T) {
			p, r, c := fixture(t)
			out := create(t, p, r, c)
			switch kind {
			case "ignored":
				if e := os.WriteFile(filepath.Join(r.Common.Path, "info/exclude"), []byte("private.data\n"), 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(r.Path, "private.data"), []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			case "untracked":
				if e := os.WriteFile(filepath.Join(r.Path, "private.data"), []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			case "unknown-admin":
				admin, _, e := p.run(context.Background(), r.Path, "rev-parse", "--path-format=absolute", "--git-dir")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(trim(admin), "private.data"), []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			case "locked":
				if _, _, e := p.run(context.Background(), r.Source.Path, "worktree", "lock", "--", r.Path); e != nil {
					t.Fatal(e)
				}
			}
			s, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0])
			if e != nil || !s.Complete || !(s.Ignored || s.Untracked || s.Unknown || s.Locked) {
				t.Fatal(s, e)
			}
			if removed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); e == nil || removed {
				t.Fatal("unsafe work removed", removed, e)
			}
			if _, e = os.Lstat(r.Path); e != nil {
				t.Fatal("attachment lost", e)
			}
		})
	}
}
func TestMissingParentAndUnresolvedBranchNeverCreate(t *testing.T) {
	p, r, c := fixture(t)
	missing := filepath.Join(r.Base.Path, "missing")
	r.Base.Path = missing
	r.Base.MutationIdentity = missing
	r.Path = filepath.Join(missing, "work")
	c.HeldLocks[2].CanonicalID = missing
	if _, got := repositories.Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused {
		t.Fatal(got)
	}
	if _, changed, e := p.Create(context.Background(), r, c.Validate); e == nil || changed {
		t.Fatal(changed, e)
	}
	if _, e := os.Lstat(missing); !os.IsNotExist(e) {
		t.Fatal("parent created", e)
	}
	p, r, c = fixture(t)
	r.Branch = ""
	if _, changed, e := p.Create(context.Background(), r, c.Validate); e == nil || changed {
		t.Fatal("detached fallback", changed, e)
	}
	if _, e := os.Lstat(r.Path); !os.IsNotExist(e) {
		t.Fatal("unexpected worktree", e)
	}
}
func TestCallbackCustodyAndFilterChangesRefused(t *testing.T) {
	for _, kind := range []string{"parent", "filter"} {
		t.Run(kind, func(t *testing.T) {
			p, r, _ := fixture(t)
			calls := 0
			_, changed, e := p.Create(context.Background(), r, func(context.Context) error {
				calls++
				if kind == "parent" {
					return os.Rename(r.Base.Path, r.Base.Path+"-previous")
				}
				_, _, e := p.run(context.Background(), r.Source.Path, "config", "filter.fixture.smudge", "fixture-unavailable-command")
				return e
			})
			if e == nil || changed || calls != 1 {
				t.Fatal(changed, e, calls)
			}
			if _, e := os.Lstat(r.Path); !os.IsNotExist(e) {
				t.Fatal("unexpected worktree", e)
			}
		})
	}
}
func TestPortCapabilitiesAndDiagnosticBound(t *testing.T) {
	p, r, _ := fixture(t)
	r.Mode = repositories.Checkout
	if p.Supported(r) {
		t.Fatal("private checkout supported")
	}
	r.Existing = true
	r.Branch = "main"
	if !p.Supported(r) {
		t.Fatal("existing checkout unsupported")
	}
	p.platform = "windows"
	if p.Supported(r) {
		t.Fatal("unsupported platform accepted")
	}
	var b limitedBuffer
	if _, e := b.Write([]byte(strings.Repeat("x", maxOutput+1))); e == nil || b.Len() != 0 {
		t.Fatal("unbounded command output")
	}
}

func TestAdvancedHeadResumesWithoutResetAndNeedsAcceptedHeadForRemoval(t *testing.T) {
	p, r, c := fixture(t)
	out := create(t, p, r, c)
	if e := os.WriteFile(filepath.Join(r.Path, "tracked.txt"), []byte("advanced\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "advance"}} {
		if _, _, e := p.run(context.Background(), r.Path, args...); e != nil {
			t.Fatal(e)
		}
	}
	observed, e := p.Observe(context.Background(), r)
	if e != nil || observed.Head == r.BaseCommit {
		t.Fatal(observed, e)
	}
	resumed := repositories.InspectResume(context.Background(), r, c.PreflightContext, p, out.Evidence)
	if resumed.Outcome != effects.AlreadyPresent {
		t.Fatal(resumed)
	}
	if changed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); e == nil || changed {
		t.Fatal("unaccepted head removed", changed, e)
	}
	actual, e := os.ReadFile(filepath.Join(r.Path, "tracked.txt"))
	if e != nil || string(actual) != "advanced\n" {
		t.Fatal("advanced work lost", e)
	}
}
func TestReadonlyAndExistingUserCheckoutAttachWithoutMutation(t *testing.T) {
	for _, mode := range []repositories.Mode{repositories.Readonly, repositories.Checkout} {
		t.Run(string(mode), func(t *testing.T) {
			p, r, c := fixture(t)
			r.Mode = mode
			r.Existing = true
			r.Branch = "main"
			r.Ownership = repositories.UserOwned
			r.Path = r.Source.Path
			r.Base.Path = filepath.Dir(r.Path)
			r.Base.MutationIdentity = r.Base.Path
			c.HeldLocks[2].CanonicalID = r.Base.Path
			for i := range c.HeldLocks {
				c.HeldLocks[i].Namespace = filepath.Join(filepath.Dir(r.Base.Path), "locks")
			}
			r.UserWriteAuthorizationID = "write-grant"
			r.UserWriteAuthorizationVersion = "revision"
			r.Readonly = repositories.ReadonlyEvidence{Path: r.Path, CommonPath: r.Common.Path, ID: "sandbox", Revision: "revision", Provenance: "host", Enforced: true}
			prepared, pre := repositories.Preflight(context.Background(), r, c.PreflightContext, p)
			if pre.Outcome != effects.Prepared {
				t.Fatal(pre)
			}
			got := repositories.Apply(context.Background(), prepared, c, p)
			if got.Outcome != effects.AlreadyPresent || got.Evidence.Attachments[0].Created {
				t.Fatal(got)
			}
		})
	}
}
func TestSafetyRefusesConfiguredFiltersBeforeStatus(t *testing.T) {
	p, r, c := fixture(t)
	out := create(t, p, r, c)
	if _, _, e := p.run(context.Background(), r.Source.Path, "config", "filter.fixture.clean", "fixture-unavailable-command"); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0]); e == nil {
		t.Fatal("filter-enabled safety inspection accepted")
	}
}
func TestPopulatedPrivateRefNamespaceRetained(t *testing.T) {
	p, r, c := fixture(t)
	out := create(t, p, r, c)
	admin, _, e := p.run(context.Background(), r.Path, "rev-parse", "--path-format=absolute", "--git-dir")
	if e != nil {
		t.Fatal(e)
	}
	refs := filepath.Join(trim(admin), "refs")
	if e = os.MkdirAll(refs, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(refs, "private-head"), []byte(r.BaseCommit+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0])
	if e != nil || !s.Complete || !s.Unknown {
		t.Fatal(s, e)
	}
	if removed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); e == nil || removed {
		t.Fatal("private refs lost", removed, e)
	}
}

func TestMutationCommandsHaveNoNetworkForcePruneOrDetachedFallback(t *testing.T) {
	p, r, c := fixture(t)
	var commands [][]string
	p.trace = func(args []string) { commands = append(commands, args) }
	out := create(t, p, r, c)
	if removed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); e != nil || !removed {
		t.Fatal(removed, e)
	}
	adds, removes := 0, 0
	for _, args := range commands {
		for _, arg := range args {
			switch arg {
			case "fetch", "clone", "pull", "push", "prune", "reset", "clean", "--force", "-f", "--detach":
				t.Fatal("forbidden Git operation", args)
			}
		}
		if len(args) > 1 && args[0] == "worktree" && args[1] == "add" {
			adds++
			expected := []string{"worktree", "add", "-b", r.Branch, "--", r.Path, r.BaseCommit}
			if strings.Join(args, "\x00") != strings.Join(expected, "\x00") {
				t.Fatal("unexpected creation arguments", args)
			}
		}
		if len(args) > 1 && args[0] == "worktree" && args[1] == "remove" {
			removes++
			expected := []string{"worktree", "remove", "--", r.Path}
			if strings.Join(args, "\x00") != strings.Join(expected, "\x00") {
				t.Fatal("unexpected removal arguments", args)
			}
		}
	}
	if adds != 1 || removes != 1 {
		t.Fatal("missing creation/removal witness", adds, removes)
	}
}

func TestAmbientGitRedirectsCannotChangeExplicitSource(t *testing.T) {
	p, r, _ := fixture(t)
	foreign := filepath.Join(filepath.Dir(r.Source.Path), "foreign")
	if e := os.Mkdir(foreign, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("GIT_DIR", foreign)
	t.Setenv("GIT_WORK_TREE", foreign)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(foreign, "index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", foreign)
	observed, e := p.Source(context.Background(), r)
	if e != nil || observed.Path != r.Source.Path || observed.Head != r.BaseCommit {
		t.Fatal("ambient Git redirects affected source", observed, e)
	}
}

func TestUnwritableMutationRootsRefusedBeforeCreatingLeaf(t *testing.T) {
	for _, which := range []string{"base", "common"} {
		t.Run(which, func(t *testing.T) {
			p, r, c := fixture(t)
			path := r.Base.Path
			if which == "common" {
				path = r.Common.Path
			}
			if e := os.Chmod(path, 0500); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { os.Chmod(path, 0700) })
			if _, got := repositories.Preflight(context.Background(), r, c.PreflightContext, p); got.Outcome != effects.Refused {
				t.Fatal(got)
			}
			if _, changed, e := p.Create(context.Background(), r, c.Validate); e == nil || changed {
				t.Fatal("unwritable root accepted", changed, e)
			}
			if _, e := os.Lstat(r.Path); !os.IsNotExist(e) {
				t.Fatal("leaf created", e)
			}
		})
	}
}

func TestSharedMetadataLockRefusesRemovalBeforeGitMutation(t *testing.T) {
	p, r, c := fixture(t)
	out := create(t, p, r, c)
	lock := filepath.Join(r.Common.Path, "index.lock")
	if e := os.WriteFile(lock, []byte("in-flight\n"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0])
	if e != nil || !s.Complete || !s.Locked || s.Unknown {
		t.Fatal(s, e)
	}
	if changed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); e == nil || changed {
		t.Fatal("shared lock did not retain attachment", changed, e)
	}
	if _, e := os.Lstat(r.Path); e != nil {
		t.Fatal(e)
	}
}

func TestHiddenTrackedEditsRetainIndexFlagsAndBytes(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			p, r, c := fixture(t)
			out := create(t, p, r, c)
			if _, _, e := p.run(context.Background(), r.Path, "update-index", flag, "--", "tracked.txt"); e != nil {
				t.Fatal(e)
			}
			file := filepath.Join(r.Path, "tracked.txt")
			want := []byte("unshipped private work\n")
			if e := os.WriteFile(file, want, 0600); e != nil {
				t.Fatal(e)
			}
			before, _, e := p.run(context.Background(), r.Path, "ls-files", "-v", "-z")
			if e != nil {
				t.Fatal(e)
			}
			s, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0])
			if e == nil && s.Complete && !s.Unknown && !s.Dirty {
				t.Fatalf("hidden edit classified safe: %+v", s)
			}
			if changed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); e == nil || changed {
				t.Fatal("hidden work removed", changed, e)
			}
			b, e := os.ReadFile(file)
			if e != nil || string(b) != string(want) {
				t.Fatal("hidden bytes lost", e)
			}
			after, _, e := p.run(context.Background(), r.Path, "ls-files", "-v", "-z")
			if e != nil || after != before {
				t.Fatal("index flags changed", e)
			}
		})
	}
}

func TestIndexFlagInspectionFailureRetainsAttachment(t *testing.T) {
	for _, kind := range []string{"error", "unterminated", "unknown", "missing-name", "empty-record"} {
		t.Run(kind, func(t *testing.T) {
			p, r, c := fixture(t)
			out := create(t, p, r, c)
			original := p.executable
			action := map[string]string{"error": "exit 2", "unterminated": "printf 'H tracked.txt'", "unknown": "printf 'X tracked.txt\\000'", "missing-name": "printf 'H \\000'"}[kind]
			if kind == "empty-record" {
				action = "printf '\\000'"
			}
			wrapper := filepath.Join(t.TempDir(), "git-inspect")
			script := "#!/bin/sh\ncase \"$*\" in *'ls-files -v -z'*) " + action + "; exit 0;; esac\nexec '" + strings.ReplaceAll(original, "'", "'\\''") + "' \"$@\"\n"
			if e := os.WriteFile(wrapper, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			p.executable = wrapper
			s, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0])
			if kind == "error" && (!s.Unknown || s.Complete || e == nil) {
				t.Fatal("index query failure did not retain unknown", s, e)
			}
			if e == nil && s.Complete && !s.Unknown {
				t.Fatal("index inspection failure accepted", s)
			}
			if changed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); changed || e == nil {
				t.Fatal("index inspection failure removed work", changed, e)
			}
			if _, e := os.Lstat(r.Path); e != nil {
				t.Fatal("attachment lost", e)
			}
		})
	}
}

func TestRetirementRetainsPlumbingReflogCommit(t *testing.T) {
	p, r, c := fixture(t)
	created := create(t, p, r, c)
	run := func(args ...string) string {
		t.Helper()
		out, _, err := p.run(context.Background(), r.Path, args...)
		if err != nil {
			t.Fatal(args, err)
		}
		return trim(out)
	}
	run("checkout", "--detach", r.BaseCommit)
	tree := run("rev-parse", "HEAD^{tree}")
	unshipped := run("commit-tree", tree, "-p", r.BaseCommit, "-m", "unshipped detached commit")
	run("update-ref", "-m", "private work", "HEAD", unshipped)
	run("checkout", r.Branch)
	if refs := run("for-each-ref", "--contains", unshipped, "--format=%(refname)"); refs != "" {
		t.Fatal("unexpected ref", refs)
	}
	if log := run("reflog", "--format=%H", "HEAD"); !strings.Contains(log, unshipped) {
		t.Fatal("missing reflog", log)
	}
	safety, err := p.Safety(context.Background(), r, created.Evidence.Attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	ret := repositories.Retirement{Header: effects.Header{Version: effects.SchemaVersion, OperationID: "retire", InputDigest: "retire-input"}, Attachment: r, Receipt: created.Evidence, AcceptedHead: r.BaseCommit, ShippedProofID: "shipped", ShippedProofRevision: "1", UnusedProofID: "unused", UnusedProofRevision: "1", Provenance: "host", AuthorizationID: "retire", AuthorizationVersion: "1"}
	c.Header = ret.Header
	ticket, pre := repositories.CheckRetirement(context.Background(), ret, c.PreflightContext, p)
	t.Logf("safety=%+v preflight=%s", safety, pre.Outcome)
	if pre.Outcome == effects.Prepared {
		removed := repositories.Retire(context.Background(), ticket, c, p)
		after, _, e := p.run(context.Background(), r.Source.Path, "rev-list", "--all", "--reflog")
		t.Fatalf("unshipped detached commit permitted retirement: result=%s private_commit_reachable=%t read_error=%v", removed.Outcome, strings.Contains(after, unshipped), e)
	}
}

func TestPrivateHistoryOIDsAndMalformedRecordsRetain(t *testing.T) {
	for _, kind := range []string{"old", "new", "original-head", "malformed-log", "malformed-original-head", "missing-log"} {
		t.Run(kind, func(t *testing.T) {
			p, r, c := fixture(t)
			out := create(t, p, r, c)
			run := func(args ...string) string {
				t.Helper()
				s, _, e := p.run(context.Background(), r.Path, args...)
				if e != nil {
					t.Fatal(args, e)
				}
				return trim(s)
			}
			tree := run("rev-parse", "HEAD^{tree}")
			orphan := run("commit-tree", tree, "-p", r.BaseCommit, "-m", "private preserved work")
			if run("for-each-ref", "--contains", orphan, "--format=%(refname)") != "" {
				t.Fatal("private commit has durable ref")
			}
			admin := run("rev-parse", "--path-format=absolute", "--git-dir")
			log := filepath.Join(admin, "logs", "HEAD")
			originalHead := filepath.Join(admin, "ORIG_HEAD")
			target := log
			text := orphan + " " + r.BaseCommit + " Fixture <fixture@example.invalid> 1700000000 +0000\tprivate transition\n"
			switch kind {
			case "new":
				text = r.BaseCommit + " " + orphan + " Fixture <fixture@example.invalid> 1700000000 +0000\tprivate transition\n"
			case "original-head":
				target = originalHead
				text = orphan + "\n"
			case "malformed-log":
				text = "malformed private history\n"
			case "malformed-original-head":
				target = originalHead
				text = "unknown private reference\n"
			case "missing-log":
				if e := os.Remove(log); e != nil {
					t.Fatal(e)
				}
			}
			if kind != "missing-log" {
				if e := os.WriteFile(target, []byte(text), 0600); e != nil {
					t.Fatal(e)
				}
			}
			s, e := p.Safety(context.Background(), r, out.Evidence.Attachments[0])
			if e == nil && s.Complete && !s.Unknown && s.Unreachable == 0 {
				t.Fatal("private history accepted", kind, s)
			}
			if changed, e := p.Remove(context.Background(), r, out.Evidence.Attachments[0], c.Validate); changed || e == nil {
				t.Fatal("private history lost", kind, changed, e)
			}
			if kind != "missing-log" {
				b, e := os.ReadFile(target)
				if e != nil || string(b) != text {
					t.Fatal("private history mutated", kind, e)
				}
			}
			if _, e := os.Lstat(r.Path); e != nil {
				t.Fatal("attachment lost", e)
			}
		})
	}
}

func TestPrivateCommitPreservedByCommonRefPermitsRetirement(t *testing.T) {
	p, r, c := fixture(t)
	done := create(t, p, r, c)
	run := func(dir string, args ...string) string {
		t.Helper()
		s, _, e := p.run(context.Background(), dir, args...)
		if e != nil {
			t.Fatal(args, e)
		}
		return trim(s)
	}
	tree := run(r.Path, "rev-parse", "HEAD^{tree}")
	saved := run(r.Path, "commit-tree", tree, "-p", r.BaseCommit, "-m", "preserved private commit")
	run(r.Path, "update-ref", "refs/heads/preserved", saved)
	admin := run(r.Path, "rev-parse", "--path-format=absolute", "--git-dir")
	if e := os.WriteFile(filepath.Join(admin, "ORIG_HEAD"), []byte(saved+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	text := saved + " " + r.BaseCommit + " Fixture <fixture@example.invalid> 1700000000 +0000\tpreserved transition\n"
	if e := os.WriteFile(filepath.Join(admin, "logs", "HEAD"), []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := p.Safety(context.Background(), r, done.Evidence.Attachments[0])
	if e != nil || !s.Complete || s.Unknown || s.Unreachable != 0 {
		t.Fatal(s, e)
	}
	if changed, e := p.Remove(context.Background(), r, done.Evidence.Attachments[0], c.Validate); !changed || e != nil {
		t.Fatal(changed, e)
	}
	if run(r.Source.Path, "rev-parse", "refs/heads/preserved") != saved {
		t.Fatal("common preservation ref lost")
	}
}

func TestRemovalSafetyRevocationStopsBeforeGitEffect(t *testing.T) {
	p, r, c := fixture(t)
	done := create(t, p, r, c)
	revoked := false
	mutations := 0
	p.trace = func(args []string) {
		if len(args) > 0 && args[0] == "rev-list" && strings.Contains(strings.Join(args, " "), "--not") {
			revoked = true
		}
		if len(args) > 1 && args[0] == "worktree" && args[1] == "remove" {
			mutations++
		}
	}
	validate := func(context.Context) error {
		if revoked {
			return errGit
		}
		return nil
	}
	if changed, e := p.Remove(context.Background(), r, done.Evidence.Attachments[0], validate); changed || e == nil || mutations != 0 {
		t.Fatal("revoked removal grant executed", changed, e, mutations)
	}
	if _, e := os.Lstat(r.Path); e != nil {
		t.Fatal(e)
	}
}

func TestFinalRemovalCallbackCannotHideTrackedEdit(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			p, r, c := fixture(t)
			done := create(t, p, r, c)
			calls := 0
			mutations := 0
			flagState := ""
			p.trace = func(args []string) {
				if len(args) > 1 && args[0] == "worktree" && args[1] == "remove" {
					mutations++
				}
			}
			validate := func(context.Context) error {
				calls++
				if calls == 2 {
					if _, _, e := p.run(context.Background(), r.Path, "update-index", flag, "--", "tracked.txt"); e != nil {
						return e
					}
					if e := os.WriteFile(filepath.Join(r.Path, "tracked.txt"), []byte("late callback unshipped work\n"), 0600); e != nil {
						return e
					}
					var e error
					flagState, _, e = p.run(context.Background(), r.Path, "ls-files", "-v", "-z")
					return e
				}
				return nil
			}
			changed, e := p.Remove(context.Background(), r, done.Evidence.Attachments[0], validate)
			if changed || e == nil || mutations != 0 {
				t.Fatalf("late callback bypassed safety: calls=%d changed=%t error=%v mutations=%d", calls, changed, e, mutations)
			}
			b, e := os.ReadFile(filepath.Join(r.Path, "tracked.txt"))
			if e != nil || string(b) != "late callback unshipped work\n" {
				t.Fatal("late edit lost", e)
			}
			after, _, e := p.run(context.Background(), r.Path, "ls-files", "-v", "-z")
			if e != nil || after != flagState {
				t.Fatal("late index flags changed", e)
			}
		})
	}
}
