//go:build linux

package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These zero-agent owned kernel controls exercise file effects and journal
// retention. Their private observation/host stubs are not public isolation or
// authorization producers, and cannot establish real-agent capture readiness.
type ownedRestoreHost struct {
	failPhase string
	closed    bool
	records   []SnapshotEffect
}

func (h *ownedRestoreHost) AcquireSnapshot(context.Context, SnapshotOperation) (SnapshotAdmission, error) {
	return h, nil
}
func (h *ownedRestoreHost) Verify(context.Context, SnapshotOperation) error { return nil }
func (h *ownedRestoreHost) Record(_ context.Context, _ SnapshotOperation, e SnapshotEffect) error {
	h.records = append(h.records, e)
	if e.Phase == h.failPhase {
		return ErrAdmissionUnavailable
	}
	return nil
}
func (h *ownedRestoreHost) Close() error { h.closed = true; return nil }
func expectedBytes(b []byte) ExpectedFile {
	sum := sha256.Sum256(b)
	return ExpectedFile{Present: true, SHA256: hex.EncodeToString(sum[:])}
}
func ownedRestoreFixture(t *testing.T) (*GuardedProvider, *RetainedSet, *ownedRestoreHost, string, RestoreRequest) {
	t.Helper()
	config := ownedGuardedConfig(t)
	root := config.Targets.roots[0].Binding.Root
	if e := os.WriteFile(filepath.Join(root, "other.txt"), []byte("captured second"), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := newOwnedGuardedProvider(config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { p.Close() })
	intent := testCaptureIntent("restore-source")
	intent.TargetMapDigest = config.Targets.Digest()
	capture, e := p.CaptureBound(context.Background(), intent)
	if e != nil {
		t.Fatal(e)
	}
	retained, e := capture.Retained()
	if e != nil {
		t.Fatal(e)
	}
	host := &ownedRestoreHost{}
	p.host = host
	p.isolation = &IsolationProof{store: config.StorePath, storeID: "owned-kernel", digest: "owned-kernel", targetDigest: config.Targets.Digest(), check: func(context.Context) error { return p.guard.check() }}
	for _, name := range []string{"source.txt", "other.txt"} {
		if e = os.WriteFile(filepath.Join(root, name), []byte("current "+name), 0600); e != nil {
			t.Fatal(e)
		}
	}
	intent.OperationID = "restore-operation"
	request := RestoreRequest{Intent: intent, Selections: []RestoreSelection{{TargetID: "root", Path: "source.txt", Expected: expectedBytes([]byte("current source.txt"))}, {TargetID: "root", Path: "other.txt", Expected: expectedBytes([]byte("current other.txt"))}}}
	return p, retained, host, root, request
}
func TestSelectiveRestoreKernelAllConflictsBeforeAnyEffect(t *testing.T) {
	for _, kind := range []string{"hash", "presence", "parent_symlink", "root_replaced"} {
		t.Run(kind, func(t *testing.T) {
			p, r, _, root, request := ownedRestoreFixture(t)
			switch kind {
			case "hash":
				request.Selections[1].Expected = expectedBytes([]byte("different"))
			case "presence":
				request.Selections[1].Expected = ExpectedFile{}
			case "parent_symlink":
				if e := os.Mkdir(filepath.Join(root, "nested"), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(root, filepath.Join(root, "link")); e != nil {
					t.Fatal(e)
				}
				request.Selections[1].Path = "link/other.txt"
			case "root_replaced":
				if e := os.Rename(root, root+"-old"); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(root, 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(root, "source.txt"), []byte("replacement"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			before, e := os.ReadFile(filepath.Join(root, "source.txt"))
			if e != nil {
				t.Fatal(e)
			}
			result, e := p.RestoreSelective(context.Background(), r, request)
			if e == nil || result.ChangedCount != 0 || result.Partial {
				t.Fatalf("conflict had effects: %+v %v", result, e)
			}
			after, e := os.ReadFile(filepath.Join(root, "source.txt"))
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("first selection changed before later conflict")
			}
		})
	}
}
func TestSelectiveRestoreKernelCompleteAndAbsent(t *testing.T) {
	p, r, _, root, request := ownedRestoreFixture(t)
	if e := os.Remove(filepath.Join(root, "other.txt")); e != nil {
		t.Fatal(e)
	}
	request.Selections[1].Expected = ExpectedFile{}
	result, e := p.RestoreSelective(context.Background(), r, request)
	if e != nil || result.Outcome != "complete" || result.ChangedCount != 2 || result.Partial {
		t.Fatalf("restore: %+v %v", result, e)
	}
	for name, expected := range map[string]string{"source.txt": "eligible owned content\n", "other.txt": "captured second"} {
		actual, e := os.ReadFile(filepath.Join(root, name))
		if e != nil || string(actual) != expected {
			t.Fatal("captured content not restored")
		}
	}
	if _, e = p.RestoreSelective(context.Background(), r, request); e == nil {
		t.Fatal("completed restore replayed")
	}
}
func TestSelectiveRestoreKernelPartialPersistenceRetainsEvidenceAndRefusesRetry(t *testing.T) {
	p, r, host, root, request := ownedRestoreFixture(t)
	host.failPhase = "file_replaced"
	result, e := p.RestoreSelective(context.Background(), r, request)
	if !errors.Is(e, ErrRestoreUncertain) || result.Outcome != "uncertain" || !result.Partial || result.ChangedCount != 1 || len(result.Obligations) == 0 {
		t.Fatalf("partial accounting: %+v %v", result, e)
	}
	first, _ := os.ReadFile(filepath.Join(root, "source.txt"))
	second, _ := os.ReadFile(filepath.Join(root, "other.txt"))
	if string(first) != "eligible owned content\n" || string(second) != "current other.txt" {
		t.Fatal("effect boundary wrong")
	}
	ledger, e := p.admission.load()
	if e != nil {
		t.Fatal(e)
	}
	if ledger.Pins[pinKey(r.ID(), request.Intent.OperationID, PinFork)].Completion != "" {
		t.Fatal("uncertain pin completed")
	}
	entries, e := os.ReadDir(p.guard.path)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "restore-") {
			raw, e := os.ReadFile(filepath.Join(p.guard.path, entry.Name()))
			if e != nil || !bytes.Contains(raw, []byte("replace_intent")) {
				t.Fatal("durable pre-effect intent missing")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("restore intent discarded")
	}
	request.Selections[0].Expected = expectedBytes(first)
	host.failPhase = ""
	if _, e = p.RestoreSelective(context.Background(), r, request); e == nil {
		t.Fatal("uncertain operation repeated effect")
	}
}
