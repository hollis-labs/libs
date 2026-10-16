//go:build linux

package snapshot

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// ObserveIsolation observes a host-frozen, provider-only cgroup. It performs no
// freeze, process launch, namespace change or signal. Unsupported or incomplete
// kernel evidence refuses; host authority and continuous admission are separate.
func ObserveIsolation(ctx context.Context, req IsolationRequest) (*IsolationProof, error) {
	if ctx == nil || req.ControlGroup == nil || req.ProcessID <= 0 || req.StartTime == 0 || req.Targets.Digest() == "" || len(req.ProtectedPaths) == 0 || !physicalDirectory(req.StorePath) {
		return nil, ErrStoreCustodyUnsupported
	}
	independentControl := false
	for _, path := range req.ProtectedPaths {
		if path != req.StorePath {
			independentControl = true
		}
	}
	if !independentControl {
		return nil, ErrStoreCustodyUnsupported
	}
	protected, err := observeProtectedRoots(req.StorePath, req.ProtectedPaths)
	if err != nil {
		return nil, err
	}
	req.ProtectedPaths = slices.Clone(req.ProtectedPaths)
	fd, err := unix.FcntlInt(req.ControlGroup.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, ErrStoreCustodyUnsupported
	}
	group := os.NewFile(uintptr(fd), "snapshot-cgroup")
	var st unix.Statfs_t
	info, err := group.Stat()
	if err != nil || !info.IsDir() || unix.Fstatfs(fd, &st) != nil || uint64(st.Type) != uint64(unix.CGROUP2_SUPER_MAGIC) {
		group.Close()
		return nil, ErrStoreCustodyUnsupported
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil || !physicalDirectory(path) {
		group.Close()
		return nil, ErrStoreCustodyUnsupported
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		group.Close()
		return nil, ErrStoreCustodyUnsupported
	}
	held, err := root.Stat(".")
	if err != nil || !os.SameFile(info, held) {
		root.Close()
		group.Close()
		return nil, ErrStoreCustodyUnsupported
	}
	pidfd, err := unix.PidfdOpen(req.ProcessID, 0)
	if err != nil {
		root.Close()
		group.Close()
		return nil, ErrStoreCustodyUnsupported
	}
	var mu sync.Mutex
	closed := false
	var baseline string
	check := func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if closed || ctx.Err() != nil {
			return ErrStoreCustodyUnsupported
		}
		ready, e := unix.Poll([]unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}, 0)
		if e != nil || ready != 0 {
			return ErrStoreCustodyUnsupported
		}
		named, e := os.Stat(path)
		if e != nil || !physicalDirectory(path) || !os.SameFile(info, named) {
			return ErrStoreCustodyUnsupported
		}
		pids, e := frozenProcesses(ctx, root)
		if e != nil || len(pids) == 0 || len(pids) > 1024 {
			return ErrStoreCustodyUnsupported
		}
		metadata, e := protected.current(ctx)
		if e != nil {
			return e
		}
		found := false
		var identities strings.Builder
		for _, pid := range pids {
			ticks, e := processTicks(pid)
			if e != nil {
				return ErrStoreCustodyUnsupported
			}
			if pid == req.ProcessID {
				found = ticks == req.StartTime
			}
			if e = isolatedProtectedProcess(ctx, pid, protected, metadata); e != nil {
				return e
			}
			fmt.Fprintf(&identities, "%d:%d;", pid, ticks)
			for _, kind := range []string{"user", "mnt", "pid"} {
				identity, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", pid, kind))
				if e != nil {
					return ErrStoreCustodyUnsupported
				}
				identities.WriteString(identity)
			}
		}
		if !found {
			return ErrStoreCustodyUnsupported
		}
		for _, scope := range req.Targets.roots {
			current, e := os.Stat(scope.Binding.Root)
			if e != nil || !physicalDirectory(scope.Binding.Root) || !os.SameFile(scope.identity, current) {
				return ErrStoreCustodyUnsupported
			}
		}
		current, e := os.Stat(req.StorePath)
		if e != nil || !physicalDirectory(req.StorePath) {
			return ErrStoreCustodyUnsupported
		}
		fmt.Fprintf(&identities, "%s:%s:%s", admissionStoreIdentity(current), admissionStoreIdentity(info), req.Targets.Digest()+protected.digest)
		sum := sha256.Sum256([]byte(identities.String()))
		digest := hex.EncodeToString(sum[:])
		if baseline == "" {
			baseline = digest
		} else if digest != baseline {
			return ErrStoreCustodyUnsupported
		}
		return nil
	}
	if err = check(ctx); err != nil {
		unix.Close(pidfd)
		root.Close()
		group.Close()
		return nil, err
	}
	storeInfo, storeErr := os.Stat(req.StorePath)
	if storeErr != nil {
		unix.Close(pidfd)
		root.Close()
		group.Close()
		return nil, ErrStoreCustodyUnsupported
	}
	storeSum := sha256.Sum256([]byte(admissionStoreIdentity(storeInfo)))
	proof := &IsolationProof{storeID: hex.EncodeToString(storeSum[:]), store: filepath.Clean(req.StorePath), targetDigest: req.Targets.Digest(), digest: baseline, protectedDigest: protected.digest, check: check}
	proof.close = func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return nil
		}
		closed = true
		unix.Close(pidfd)
		root.Close()
		return group.Close()
	}
	return proof, nil
}

func boundedMetadata(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", ErrStoreCustodyUnsupported
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return "", ErrStoreCustodyUnsupported
	}
	return string(b), nil
}
func rootMetadata(root *os.Root, path string) (string, error) {
	f, e := root.Open(path)
	if e != nil {
		return "", ErrStoreCustodyUnsupported
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return "", ErrStoreCustodyUnsupported
	}
	return string(b), nil
}
func frozenProcesses(ctx context.Context, root *os.Root) ([]int, error) {
	var pids []int
	seen := map[int]bool{}
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if ctx.Err() != nil || depth > 32 {
			return ErrStoreCustodyUnsupported
		}
		groupType, e := rootMetadata(root, filepath.Join(path, "cgroup.type"))
		if e != nil || strings.TrimSpace(groupType) != "domain" {
			return ErrStoreCustodyUnsupported
		}
		events, e := rootMetadata(root, filepath.Join(path, "cgroup.events"))
		if e != nil || !strings.Contains("\n"+events, "\nfrozen 1\n") {
			return ErrStoreCustodyUnsupported
		}
		raw, e := rootMetadata(root, filepath.Join(path, "cgroup.procs"))
		if e != nil {
			return e
		}
		for _, s := range strings.Fields(raw) {
			pid, e := strconv.Atoi(s)
			if e != nil || pid <= 0 || seen[pid] || len(pids) >= 1024 {
				return ErrStoreCustodyUnsupported
			}
			seen[pid] = true
			pids = append(pids, pid)
		}
		d, e := root.Open(path)
		if e != nil {
			return ErrStoreCustodyUnsupported
		}
		entries, e := d.ReadDir(4097)
		d.Close()
		if e != nil && e != io.EOF || len(entries) > 4096 {
			return ErrStoreCustodyUnsupported
		}
		for _, entry := range entries {
			if entry.IsDir() {
				if e = walk(filepath.Join(path, entry.Name()), depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := walk(".", 0); e != nil {
		return nil, e
	}
	return pids, nil
}
func processTicks(pid int) (uint64, error) {
	raw, e := boundedMetadata(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return 0, e
	}
	last := strings.LastIndex(raw, ")")
	if last < 0 {
		return 0, ErrStoreCustodyUnsupported
	}
	fields := strings.Fields(raw[last+1:])
	if len(fields) <= 19 {
		return 0, ErrStoreCustodyUnsupported
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

type kernelMount struct{ device, root, mountpoint, fs string }

func kernelMounts(raw string) ([]kernelMount, error) {
	var result []kernelMount
	scan := bufio.NewScanner(strings.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 1<<20)
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for scan.Scan() {
		line := scan.Text()
		a, b, ok := strings.Cut(line, " - ")
		left, right := strings.Fields(a), strings.Fields(b)
		if !ok || len(left) < 6 || len(right) < 3 {
			return nil, ErrStoreCustodyUnsupported
		}
		result = append(result, kernelMount{left[2], unescape.Replace(left[3]), unescape.Replace(left[4]), right[0]})
	}
	if scan.Err() != nil || len(result) == 0 {
		return nil, ErrStoreCustodyUnsupported
	}
	return result, nil
}
func kernelStoreLocation(store string) (string, string, error) {
	raw, e := boundedMetadata("/proc/self/mountinfo")
	if e != nil {
		return "", "", e
	}
	mounts, e := kernelMounts(raw)
	if e != nil {
		return "", "", e
	}
	var best kernelMount
	for _, m := range mounts {
		if pathWithin(store, m.mountpoint) && len(m.mountpoint) > len(best.mountpoint) {
			best = m
		}
	}
	if best.mountpoint == "" {
		return "", "", ErrStoreCustodyUnsupported
	}
	rel, e := filepath.Rel(best.mountpoint, store)
	if e != nil {
		return "", "", ErrStoreCustodyUnsupported
	}
	return best.device, filepath.Join(best.root, rel), nil
}
func pathWithin(child, parent string) bool {
	r, e := filepath.Rel(parent, child)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator))
}
func isolatedProcess(ctx context.Context, pid int, store string) error {
	protected, e := observeProtectedRoots(store, []string{store})
	if e != nil {
		return e
	}
	metadata, e := protected.current(ctx)
	if e != nil {
		return e
	}
	return isolatedProtectedProcess(ctx, pid, protected, metadata)
}
func isolatedProtectedProcess(ctx context.Context, pid int, protected *protectedRoots, metadata protectedMetadata) error {
	tasks, e := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if e != nil || len(tasks) == 0 || len(tasks) > 4096 {
		return ErrStoreCustodyUnsupported
	}
	for _, task := range tasks {
		if _, e := strconv.Atoi(task.Name()); e != nil || !task.IsDir() {
			return ErrStoreCustodyUnsupported
		}
		if e := isolatedThread(ctx, fmt.Sprintf("/proc/%d/task/%s", pid, task.Name()), protected, metadata); e != nil {
			return e
		}
	}
	return nil
}
func isolatedThread(ctx context.Context, base string, protected *protectedRoots, metadata protectedMetadata) error {
	if ctx.Err() != nil {
		return ErrStoreCustodyUnsupported
	}
	for _, kind := range []string{"user", "mnt", "pid"} {
		host, e := os.Readlink("/proc/self/ns/" + kind)
		native, n := os.Readlink(base + "/ns/" + kind)
		if e != nil || n != nil || native == host {
			return ErrStoreCustodyUnsupported
		}
	}
	status, e := boundedMetadata(base + "/status")
	if e != nil {
		return e
	}
	fields := map[string]string{}
	for _, line := range strings.Split(status, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			fields[k] = strings.TrimSpace(v)
		}
	}
	if fields["NoNewPrivs"] != "1" {
		return ErrStoreCustodyUnsupported
	}
	for _, key := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		v, e := strconv.ParseUint(fields[key], 16, 64)
		if e != nil || v != 0 {
			return ErrStoreCustodyUnsupported
		}
	}
	raw, e := boundedMetadata(base + "/mountinfo")
	if e != nil {
		return e
	}
	mounts, e := kernelMounts(raw)
	if e != nil {
		return e
	}
	for _, m := range mounts {
		switch m.fs {
		case "ext4", "xfs", "btrfs", "tmpfs", "proc", "sysfs", "devtmpfs", "devpts":
		default:
			return ErrStoreCustodyUnsupported
		}
		for _, root := range protected.roots {
			device, location, e := kernelStoreLocation(root.path)
			if e != nil {
				return e
			}
			if m.device == device && (pathWithin(location, m.root) || pathWithin(m.root, location)) {
				return ErrStoreCustodyUnsupported
			}
		}
	}
	// A private PID namespace alone does not make a host proc mount private.
	maps, mapErr := boundedMetadata(base + "/maps")
	if mapErr != nil || strings.Contains(maps, "[aio]") || metadata.exposedMapping(maps) {
		return ErrStoreCustodyUnsupported
	}
	for _, root := range protected.roots {
		// Deleted mappings still hold readable/writable content after their
		// inode disappears from the current directory inventory. Refuse the
		// kernel-reported protected name too; never infer absence from unlink.
		if strings.Contains(maps, root.path+"/") {
			return ErrStoreCustodyUnsupported
		}
	}
	native, e := os.Readlink(base + "/ns/pid")
	proc, e2 := os.Readlink(base + "/root/proc/1/ns/pid")
	if e != nil || e2 != nil || native != proc {
		return ErrStoreCustodyUnsupported
	}
	// Retained directory descriptors can bypass an otherwise correct mount view.
	for _, name := range []string{"cwd", "root"} {
		info, e := os.Stat(base + "/" + name)
		if e != nil || info.Mode()&os.ModeSocket != 0 || metadata.hasIdentity(info) {
			return ErrStoreCustodyUnsupported
		}
		for _, ancestor := range metadata.ancestors {
			if os.SameFile(info, ancestor) {
				return ErrStoreCustodyUnsupported
			}
		}
	}
	entries, e := os.ReadDir(base + "/fd")
	if e != nil || len(entries) > 4096 {
		return ErrStoreCustodyUnsupported
	}
	for _, entry := range entries {
		fd := base + "/fd/" + entry.Name()
		link, e := os.Readlink(fd)
		if e != nil {
			return ErrStoreCustodyUnsupported
		}
		if strings.Contains(link, "io_uring") || protected.contains(strings.TrimSuffix(link, " (deleted)")) || strings.Contains(link, ":[") && !strings.HasPrefix(link, "pipe:[") && !strings.HasPrefix(link, "anon_inode:[") {
			return ErrStoreCustodyUnsupported
		}
		info, e := os.Stat(fd)
		var fsInfo unix.Statfs_t
		if unix.Statfs(fd, &fsInfo) != nil || (uint64(fsInfo.Type) == uint64(unix.PROC_SUPER_MAGIC) || uint64(fsInfo.Type) == uint64(unix.CGROUP2_SUPER_MAGIC)) {
			return ErrStoreCustodyUnsupported
		}
		if e != nil || info.Mode()&os.ModeSocket != 0 || metadata.hasIdentity(info) {
			return ErrStoreCustodyUnsupported
		}
		for _, ancestor := range metadata.ancestors {
			if os.SameFile(info, ancestor) {
				return ErrStoreCustodyUnsupported
			}
		}
	}
	return nil
}
