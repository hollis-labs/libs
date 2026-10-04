package gitadapter

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

const maxPrivateHistoryOIDs = 256
const maxPrivateHistoryRecords = 1024

// readPrivate observes a regular file inside the pinned common metadata root.
// Missing mandatory history, symlinks, changed identity and oversized data refuse.
func readPrivate(ctx context.Context, root *os.Root, name string, optional bool) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, errGit
	}
	before, e := root.Lstat(name)
	if optional && os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil || !before.Mode().IsRegular() || before.Size() > maxOutput {
		return nil, errGit
	}
	f, e := root.OpenFile(name, os.O_RDONLY|readNonblockFlag, 0)
	if e != nil {
		return nil, errGit
	}
	observed, e := f.Stat()
	if e != nil || !os.SameFile(before, observed) {
		f.Close()
		return nil, errGit
	}
	b, e := io.ReadAll(io.LimitReader(f, maxOutput+1))
	closeErr := f.Close()
	after, statErr := root.Lstat(name)
	if e != nil || closeErr != nil || statErr != nil || len(b) > maxOutput || ctx.Err() != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errGit
	}
	return b, nil
}

// privateHistoryCount proves preservation through common branch, remote or tag
// refs. Private HEAD history and ORIG_HEAD disappear on worktree removal; all
// their old/new commits must remain reachable without those private records.
// No logs, refs, index flags or user bytes are modified to establish this proof.
func (p *Port) privateHistoryCount(ctx context.Context, r repositories.Request, common *os.Root, admin, head string) (int, error) {
	rel, e := filepath.Rel(r.Common.Path, admin)
	if e != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, errGit
	}
	ids := map[string]bool{}
	add := func(id string, zero bool) bool {
		if !oid(id) || len(id) != len(head) {
			return false
		}
		if id == strings.Repeat("0", len(id)) {
			return zero
		}
		ids[id] = true
		return len(ids) <= maxPrivateHistoryOIDs
	}
	if !add(head, false) {
		return 0, errGit
	}
	b, e := readPrivate(ctx, common, filepath.Join(rel, "logs", "HEAD"), false)
	if e != nil || len(b) == 0 || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) || !strings.HasSuffix(string(b), "\n") {
		return 0, errGit
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) > maxPrivateHistoryRecords {
		return 0, errGit
	}
	for _, line := range lines {
		prefix, _, _ := strings.Cut(line, "\t")
		fields := strings.Fields(prefix)
		if len(fields) < 6 || !add(fields[0], true) || !add(fields[1], true) {
			return 0, errGit
		}
		actor := strings.Join(fields[2:len(fields)-2], " ")
		if !strings.Contains(actor, " <") || !strings.HasSuffix(actor, ">") {
			return 0, errGit
		}
		_, e := strconv.ParseUint(fields[len(fields)-2], 10, 64)
		zone := fields[len(fields)-1]
		if e != nil || len(zone) != 5 || (zone[0] != '+' && zone[0] != '-') {
			return 0, errGit
		}
		for _, c := range zone[1:] {
			if c < '0' || c > '9' {
				return 0, errGit
			}
		}
	}
	b, e = readPrivate(ctx, common, filepath.Join(rel, "ORIG_HEAD"), true)
	if e != nil {
		return 0, errGit
	}
	if b != nil {
		value := strings.TrimSuffix(string(b), "\n")
		if !add(value, false) {
			return 0, errGit
		}
	}
	commits := make([]string, 0, len(ids))
	for id := range ids {
		commits = append(commits, id)
	}
	sort.Strings(commits)
	args := append([]string{"rev-list", "--count"}, commits...)
	args = append(args, "--not", "--branches", "--remotes", "--tags")
	raw, _, e := p.run(ctx, r.Path, args...)
	if e != nil {
		return 0, errGit
	}
	count, e := strconv.Atoi(trim(raw))
	if e != nil || count < 0 || ctx.Err() != nil {
		return 0, errGit
	}
	return count, nil
}
