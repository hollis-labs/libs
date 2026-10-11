//go:build linux

package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsolationObservationCannotBeMintedFromMetadataOrOrdinaryDirectory(t *testing.T) {
	config := ownedGuardedConfig(t)
	group, e := os.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer group.Close()
	ticks, e := processTicks(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	for _, request := range []IsolationRequest{{}, {ProcessID: os.Getpid(), StartTime: ticks, ControlGroup: group, StorePath: config.StorePath, Targets: config.Targets}} {
		proof, e := ObserveIsolation(context.Background(), request)
		if proof != nil || !errors.Is(e, ErrStoreCustodyUnsupported) {
			t.Fatal("non-kernel or absent observation minted isolation")
		}
	}
	// Actual host namespace access is rejected even though the directory is
	// private and the process identity is real. No process/cgroup is modified.
	if e = isolatedProcess(context.Background(), os.Getpid(), config.StorePath); !errors.Is(e, ErrStoreCustodyUnsupported) {
		t.Fatal("host namespaces accepted")
	}
	if e = (&IsolationProof{}).Verify(context.Background()); !errors.Is(e, ErrStoreCustodyUnsupported) {
		t.Fatal("zero proof accepted")
	}
	entries, e := os.ReadDir(config.StorePath)
	if e != nil || len(entries) != 0 {
		t.Fatal("refusal affected store")
	}
}
func TestKernelMountStoreMappingIncludesBindMountAliases(t *testing.T) {
	mounts, e := kernelMounts("31 22 8:1 /private/store /innocent rw - ext4 /dev/example rw\n32 22 8:1 /source /work rw - ext4 /dev/example rw\n")
	if e != nil || len(mounts) != 2 {
		t.Fatal(e)
	}
	if !pathWithin("/private/store/object", mounts[0].root) || pathWithin("/private/store/object", mounts[1].root) {
		t.Fatal("bind mount filesystem-root mapping lost")
	}
	escaped, e := kernelMounts("31 22 8:1 /a\\040b /alias\\040name rw - ext4 /dev/example rw\n")
	if e != nil || escaped[0].root != "/a b" || escaped[0].mountpoint != "/alias name" {
		t.Fatal("mountinfo escape decoding")
	}
	if _, e = kernelMounts("malformed"); e == nil {
		t.Fatal("malformed metadata accepted")
	}
	if pathWithin(filepath.Join("/private", "other"), "/private/store") {
		t.Fatal("sibling path accepted")
	}
}
