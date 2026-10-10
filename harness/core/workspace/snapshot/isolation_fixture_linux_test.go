//go:build linux

package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This fixture uses the public OS observer and a newly owned zero-agent helper
// in a delegated child cgroup. It never moves/freezes a service or provider and
// does not issue host authority. Unavailable kernel prerequisites are explicit.
func TestIsolationPublicOSFixture(t *testing.T) {
	if os.Getenv("SNAPSHOT_ISOLATION_HELPER") == "1" {
		if e := os.WriteFile("/marker/ready", []byte("owned namespace helper"), 0600); e != nil {
			t.Fatal(e)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	for _, mode := range []string{"isolated", "store_alias", "host_proc", "inherited_store_fd"} {
		t.Run(mode, func(t *testing.T) { runIsolationPublicFixture(t, mode) })
	}
}
func runIsolationPublicFixture(t *testing.T, mode string) {
	bwrap, e := exec.LookPath("bwrap")
	if e != nil {
		t.Skip("public isolation fixture requires bubblewrap")
	}
	membership, e := os.ReadFile("/proc/self/cgroup")
	if e != nil {
		t.Skip("no current cgroup-v2 metadata")
	}
	var parent string
	for _, line := range strings.Split(string(membership), "\n") {
		if strings.HasPrefix(line, "0::") {
			parent = filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
		}
	}
	if parent == "" {
		t.Skip("no delegated cgroup-v2 hierarchy")
	}
	id, e := randomAdmissionID()
	if e != nil {
		t.Fatal(e)
	}
	groupPath := filepath.Join(parent, "snapshot-fixture-"+id)
	if e = os.Mkdir(groupPath, 0700); e != nil {
		t.Skip("no owned delegated child cgroup available")
	}
	t.Cleanup(func() {
		if e := os.Remove(groupPath); e != nil {
			t.Errorf("owned fixture cgroup retained: %v", e)
		}
	})
	group, e := os.Open(groupPath)
	if e != nil {
		t.Fatal(e)
	}
	defer group.Close()
	config := ownedGuardedConfig(t)
	marker := t.TempDir()
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	args := []string{"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-net", "--new-session", "--die-with-parent", "--cap-drop", "ALL", "--ro-bind", binary, "/helper", "--bind", config.Targets.roots[0].Binding.Root, "/source", "--bind", marker, "/marker", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "/helper", "-test.run=^TestIsolationPublicOSFixture$"}
	runtimeArgs := []string{}
	for _, path := range []string{"/usr", "/lib", "/lib64"} {
		if _, e := os.Stat(path); e == nil {
			runtimeArgs = append(runtimeArgs, "--ro-bind", path, path)
		}
	}
	switch mode {
	case "store_alias":
		runtimeArgs = append(runtimeArgs, "--ro-bind", config.StorePath, "/apparently-innocent")
	case "host_proc":
		// Append after the private proc mount to deliberately replace that view.
		index := len(args) - 2
		args = append(append(append([]string(nil), args[:index]...), "--ro-bind", "/proc", "/proc"), args[index:]...)
	}
	args = append(runtimeArgs, args...)
	command := exec.Command(bwrap, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "SNAPSHOT_ISOLATION_HELPER=1"}
	if mode == "inherited_store_fd" {
		inherited, e := os.Open(config.StorePath)
		if e != nil {
			t.Fatal(e)
		}
		defer inherited.Close()
		command.ExtraFiles = []*os.File{inherited}
	}
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	command.Stdout = &diagnostic
	if e = command.Start(); e != nil {
		t.Skip("owned namespace helper cannot start")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	reaped := false
	defer func() {
		// Thaw only our child group. Kill/reap only the directly owned test helper.
		_ = os.WriteFile(filepath.Join(groupPath, "cgroup.freeze"), []byte("0"), 0600)
		if !reaped {
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("owned fixture helper not reaped")
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e = os.Stat(filepath.Join(marker, "ready")); e == nil {
			break
		}
		select {
		case <-done:
			reaped = true
			message := diagnostic.String()
			if strings.Contains(message, "Operation not permitted") || strings.Contains(message, "Permission denied") || strings.Contains(message, "No permissions to create") {
				t.Skip("kernel namespace creation refused by OS; no physical proof or host authority issued")
			}
			t.Fatalf("owned namespace helper setup failed: %s", strings.TrimSpace(message))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("owned helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var payload int
	var visit func(int, int)
	visit = func(pid, depth int) {
		if depth > 8 || payload != 0 {
			return
		}
		expected, _ := os.Stat(binary)
		actual, statErr := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
		if statErr == nil && os.SameFile(expected, actual) {
			payload = pid
			return
		}
		children, e := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
		if e != nil {
			return
		}
		for _, child := range strings.Fields(string(children)) {
			n, e := strconv.Atoi(child)
			if e == nil {
				visit(n, depth+1)
			}
		}
	}
	visit(command.Process.Pid, 0)
	if payload == 0 {
		t.Fatal("owned namespace helper process not located")
	}
	if mode == "inherited_store_fd" {
		original, _ := os.Stat(config.StorePath)
		inherited, e := os.Stat(fmt.Sprintf("/proc/%d/fd/3", payload))
		if e != nil || !os.SameFile(original, inherited) {
			t.Skip("fixture launcher did not preserve actual inherited store descriptor; no FD-refusal credit")
		}
	}
	ticks, e := processTicks(payload)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(groupPath, "cgroup.procs"), []byte(strconv.Itoa(payload)), 0600); e != nil {
		t.Skip("owned helper cannot enter delegated child group")
	}
	if e = os.WriteFile(filepath.Join(groupPath, "cgroup.freeze"), []byte("1"), 0600); e != nil {
		t.Skip("owned child group freezer unavailable")
	}
	for {
		events, e := rootMetadataFromPath(groupPath, "cgroup.events")
		if e == nil && strings.Contains("\n"+events, "\nfrozen 1\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned fixture freeze not completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	proof, e := ObserveIsolation(context.Background(), IsolationRequest{ProcessID: payload, StartTime: ticks, ControlGroup: group, StorePath: config.StorePath, Targets: config.Targets})
	if mode != "isolated" {
		if proof != nil || !errors.Is(e, ErrStoreCustodyUnsupported) {
			t.Fatal("unsafe actual OS exposure issued isolation")
		}
		return
	}
	if e != nil {
		t.Fatal("genuine frozen OS fixture refused", e)
	}
	defer proof.Close()
	if proof.Digest() == "" || proof.Verify(context.Background()) != nil {
		t.Fatal("issued physical observation not revalidated")
	}
	config.Isolation = proof
	if _, e = NewGuardedProvider(config); !errors.Is(e, ErrStoreCustodyUnsupported) {
		t.Fatal("physical observation alone minted host authority")
	}
	if e = os.WriteFile(filepath.Join(groupPath, "cgroup.freeze"), []byte("0"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = proof.Verify(context.Background()); !errors.Is(e, ErrStoreCustodyUnsupported) {
		t.Fatal("thawed source retained frozen observation")
	}
}
func rootMetadataFromPath(path, name string) (string, error) {
	r, e := os.OpenRoot(path)
	if e != nil {
		return "", e
	}
	defer r.Close()
	return rootMetadata(r, name)
}
