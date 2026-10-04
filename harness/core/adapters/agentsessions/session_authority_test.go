//go:build !windows

package agentsessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/workspace"
)

func sessionRuntimes(a provider.CLIAdapter) map[string]Runtime {
	cfg := AdapterRuntimeConfig{ID: "fixture", Kind: "cli", Adapter: a}
	return map[string]Runtime{"per-turn": &adapterRuntime{cfg: cfg}, "pty": &ptyRuntime{cfg: cfg}, "streaming": &streamingStdioRuntime{cfg: cfg}, "jsonrpc": &jsonRpcStdioRuntime{cfg: cfg}, "http": &serveHTTPRuntime{cfg: cfg}}
}

func TestAllSessionPathsRequireAuthorityBeforeRendering(t *testing.T) {
	rendered := false
	a := &fakeBootDirAdapter{name: "fixture", spec: provider.BootDirSpec{PlantedFiles: []provider.PlantedFile{{RelPath: "AGENTS.md", Render: func(provider.PlantContext) (string, error) { rendered = true; return "never", nil }}}}}
	for name, r := range sessionRuntimes(a) {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "candidate")
			session, err := r.Start(context.Background(), StartOptions{AutoPlantBootDir: true, ArtifactRoot: root, Workdir: "/fixture/project"})
			var refusal *workspace.Refusal
			if session != nil || !errors.As(err, &refusal) || refusal.Status != workspace.Unsupported || rendered {
				t.Fatalf("missing authority crossed boundary: %v %v", session, err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("candidate created: %v", err)
			}
		})
	}
}

func TestSessionInvalidCustodyRefusesBeforeRenderAndCallbacks(t *testing.T) {
	for _, name := range []string{"inactive", "private custody", "root mismatch", "grant", "expired", "existing unsafe root", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			rendered, called := false, false
			records := 0
			a := &fakeBootDirAdapter{name: "fixture", spec: provider.BootDirSpec{PlantedFiles: []provider.PlantedFile{{RelPath: "AGENTS.md", Render: func(provider.PlantContext) (string, error) { rendered = true; return "never", nil }}}}}
			opts := fixtureStartOptions(t, StartOptions{AutoPlantBootDir: true, OnBootDirPlanted: func(string) { called = true }, OnArtifactPrepared: func(workspace.ApplyResult) { called = true }})
			authorize := opts.ArtifactAuthorization
			opts.ArtifactAuthorization = func(ctx context.Context, path string) (agentlaunch.ArtifactAuthority, error) {
				value, err := authorize(ctx, path)
				value.Ports.ReceiptStore = &countSessionStore{ReceiptStore: value.Ports.ReceiptStore, records: &records}
				switch name {
				case "inactive":
					value.Inactive = false
				case "private custody":
					value.PrivateCustody = false
				case "root mismatch":
					value.Input.Root.Path += "-other"
				case "expired":
					value.Input.Observed.At = time.Now().Add(-2 * time.Minute)
					value.Input.Observed.ExpiresAt = time.Now().Add(-time.Minute)
				case "existing unsafe root":
					if e := os.Mkdir(path, 0755); e != nil {
						t.Fatal(e)
					}
				case "grant":
					value.Input.Resources.Grants = nil
				}
				return value, err
			}
			ctx := context.Background()
			if name == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			root, _, _, err := preparePlant(ctx, opts, a, "fixture")
			if err == nil || root != "" || rendered || called || records != 0 {
				t.Fatalf("invalid authority crossed boundary: %s %v", root, err)
			}
			if name == "existing unsafe root" {
				info, e := os.Stat(opts.ArtifactRoot)
				if e != nil || info.Mode().Perm() != 0755 {
					t.Fatalf("candidate mode changed: %v", e)
				}
				entries, e := os.ReadDir(opts.ArtifactRoot)
				if e != nil || len(entries) != 0 {
					t.Fatalf("candidate content changed: %v", e)
				}
			} else if _, err := os.Stat(opts.ArtifactRoot); !os.IsNotExist(err) {
				t.Fatalf("candidate changed: %v", err)
			}
		})
	}
}

func TestSessionFailedArtifactRecordRetainsStructuredPartial(t *testing.T) {
	opts := fixtureStartOptions(t, StartOptions{AutoPlantBootDir: true})
	authorize := opts.ArtifactAuthorization
	opts.ArtifactAuthorization = func(ctx context.Context, path string) (agentlaunch.ArtifactAuthority, error) {
		a, err := authorize(ctx, path)
		a.Ports.ReceiptStore = &failSessionStore{ReceiptStore: a.Ports.ReceiptStore}
		return a, err
	}
	called := false
	opts.OnBootDirPlanted = func(string) { called = true }
	a := &fakeBootDirAdapter{name: "fixture", spec: provider.BootDirSpec{PlantedFiles: []provider.PlantedFile{{RelPath: "AGENTS.md", Render: staticContent("retain")}}}}
	root, _, _, err := preparePlant(context.Background(), opts, a, "fixture")
	var partial *ArtifactPreparationError
	if root != "" || !errors.As(err, &partial) || partial.Result.Status != workspace.Partial || partial.Result.ArtifactsComplete() || len(partial.Result.Retained) == 0 || len(partial.Result.Obligations) == 0 || called {
		t.Fatalf("partial accounting lost: %s %v", root, err)
	}
	if b, err := os.ReadFile(filepath.Join(opts.ArtifactRoot, "AGENTS.md")); err != nil || string(b) != "retain" {
		t.Fatalf("partial root cleaned: %v", err)
	}
}

type failSessionStore struct{ workspace.ReceiptStore }

func (s *failSessionStore) Record(ctx context.Context, r workspace.Receipt) error {
	if r.Phase == workspace.ArtifactsCommitted {
		return errors.New("fixture committed record failure")
	}
	return s.ReceiptStore.Record(ctx, r)
}

func TestSessionStartFailuresRetainCommittedRootsAcrossProtocols(t *testing.T) {
	a := &fakeBootDirAdapter{name: "fixture", spec: provider.BootDirSpec{PlantedFiles: []provider.PlantedFile{{RelPath: "AGENTS.md", Render: staticContent("retain")}}}}
	for name, r := range sessionRuntimes(a) {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			blocked := filepath.Join(base, "blocked")
			if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
				t.Fatal(err)
			}
			opts := fixtureStartOptions(t, StartOptions{AutoPlantBootDir: true, Workdir: base, LogPath: filepath.Join(blocked, "session.log")})
			var observed workspace.ApplyResult
			opts.OnArtifactPrepared = func(result workspace.ApplyResult) {
				observed = result
				if !result.ArtifactsComplete() {
					t.Error("callback before verified commit")
				}
				result.Receipt.Roots[0].Root.Path = "observer edit"
			}
			session, err := r.Start(context.Background(), opts)
			if name == "per-turn" {
				if err != nil {
					t.Fatal(err)
				}
				_ = session.Stop(context.Background())
			} else {
				var partial *ArtifactPreparationError
				if !errors.As(err, &partial) || partial.Result.Status != workspace.Partial || len(partial.Result.Retained) == 0 || partial.Result.Receipt.Roots[0].Root.Path != opts.ArtifactRoot {
					t.Fatalf("start failure lost root: %v", err)
				}
			}
			if len(observed.Handles) == 0 {
				t.Fatal("structured success accounting not observed")
			}
			if b, err := os.ReadFile(filepath.Join(opts.ArtifactRoot, "AGENTS.md")); err != nil || string(b) != "retain" {
				t.Fatalf("committed root cleaned: %v", err)
			}
		})
	}
}

type countSessionStore struct {
	workspace.ReceiptStore
	records *int
}

func (s *countSessionStore) Record(ctx context.Context, r workspace.Receipt) error {
	*s.records++
	return s.ReceiptStore.Record(ctx, r)
}
