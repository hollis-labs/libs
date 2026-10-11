package snapshot_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
)

// This host deliberately refuses: it is not a production authority issuer.
type refusingConsumerHost struct{ calls int }

func (h *refusingConsumerHost) AcquireSnapshot(context.Context, snapshot.SnapshotOperation) (snapshot.SnapshotAdmission, error) {
	h.calls++
	return nil, snapshot.ErrAdmissionUnavailable
}

type consumerRedactor struct{}

func (consumerRedactor) RedactSnapshot(_ context.Context, input []byte) ([]byte, error) {
	return append([]byte(nil), input...), nil
}

func consumerConfig(t *testing.T) snapshot.GuardedConfig {
	t.Helper()
	base := t.TempDir()
	source, store := filepath.Join(base, "source"), filepath.Join(base, "store")
	for _, path := range []string{source, store} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("unchanged consumer content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	access := sandbox.ResolvedAccessPolicy{ID: "consumer-policy", Mode: sandbox.ConfinementRequired, FS: sandbox.ResolvedFilesystemAccess{Write: []sandbox.ResolvedPath{{Path: source}}}}
	plan, err := snapshot.DeriveTargets(access, snapshot.TargetPolicy{Version: "consumer-targets", Roots: []snapshot.RootBinding{{ID: "source", Root: source, Provenance: "owned-consumer"}}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.GuardedConfig{StorePath: store, Access: access, Targets: plan, Policy: &snapshot.CapturePolicy{Revision: "consumer-v1", Budgets: snapshot.CaptureBudgets{MaxCaptureBytes: 1 << 20, MaxRootBytes: 1 << 20, MaxStorageBytes: 8 << 20, MaxRunBytes: 8 << 20, MaxCaptureEntries: 64, MaxRoots: 1, MaxRunCaptures: 2, MaxDuration: time.Second}}}
}

func assertConsumerUnchanged(t *testing.T, config snapshot.GuardedConfig) {
	t.Helper()
	entries, err := os.ReadDir(config.StorePath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refusal ingested or accounted content: entries=%v error=%v", entries, err)
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(config.StorePath), "source", "source.txt"))
	if err != nil || string(content) != "unchanged consumer content\n" {
		t.Fatal("refusal changed source content", err)
	}
}

func TestConsumerHostAssertionsCannotReplaceIsolation(t *testing.T) {
	for _, name := range []string{"policy_only", "missing_host", "zero_proof", "closed_zero_proof", "missing_redactor"} {
		t.Run(name, func(t *testing.T) {
			config := consumerConfig(t)
			host := &refusingConsumerHost{}
			config.Host, config.Redactor, config.RedactionRevision = host, consumerRedactor{}, "consumer-redaction-v1"
			switch name {
			case "policy_only":
				config.Host, config.Redactor = nil, nil
			case "missing_host":
				config.Isolation, config.Host = &snapshot.IsolationProof{}, nil
			case "zero_proof":
				config.Isolation = &snapshot.IsolationProof{}
			case "closed_zero_proof":
				config.Isolation = &snapshot.IsolationProof{}
				if err := config.Isolation.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing_redactor":
				config.Isolation, config.Redactor = &snapshot.IsolationProof{}, nil
			}
			provider, err := snapshot.NewGuardedProvider(config)
			if provider != nil || !errors.Is(err, snapshot.ErrStoreCustodyUnsupported) {
				t.Fatal("unissued isolation constructed a provider", err)
			}
			if host.calls != 0 {
				t.Fatal("unissued physical proof reached host authority")
			}
			assertConsumerUnchanged(t, config)
		})
	}
}

func TestConsumerOrdinaryDirectoryCannotImpersonateCanonicalGroup(t *testing.T) {
	config := consumerConfig(t)
	control := t.TempDir()
	group, err := os.Open(control)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		proof, err := snapshot.ObserveIsolation(ctx, snapshot.IsolationRequest{ProcessID: os.Getpid(), StartTime: 1, ControlGroup: group, StorePath: config.StorePath, ProtectedPaths: []string{control}, Targets: config.Targets})
		cancel()
		if proof != nil || !errors.Is(err, snapshot.ErrStoreCustodyUnsupported) {
			t.Fatal("real PID and private directories minted kernel isolation", err)
		}
		assertConsumerUnchanged(t, config)
	}
}
