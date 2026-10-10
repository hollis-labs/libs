package snapshot

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git object identity, not an authorization primitive
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// CapturedFile contains only material captured under the immutable target and
// secret policy. Git preserves executable bits, not arbitrary filesystem
// metadata. Symlinks/submodules/special files are unsupported, never followed.
type CapturedFile struct {
	Path  string
	Mode  fs.FileMode
	Bytes []byte
}

func (a *Admission) bindCapturedStore(sg *ShadowGit, guard *storeGuard) error {
	if a == nil || sg == nil || guard == nil || sg.baseDir != a.root || guard.path != a.root {
		return ErrAdmissionUnavailable
	}
	if err := guard.check(); err != nil {
		return err
	}
	if err := a.checkRoot(); err != nil {
		return err
	}
	a.shadow = sg
	a.guard = guard
	return nil
}

// ReadFiles requires an already-held complete set pin and concrete captured
// store custody. It never reads the source working root or uses Restore. Size
// and entry limits apply before content allocation. No unpinned JSON snapshot
// can call this method successfully.
func (l *ReadLease) ReadFiles(ctx context.Context, targetID string) ([]CapturedFile, error) {
	if err := l.Verify(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(ctx, l.deadline)
	defer cancel()
	a := l.admission
	if a.shadow == nil || a.guard == nil {
		return nil, ErrAdmissionUnavailable
	}
	if err := a.guard.check(); err != nil {
		return nil, err
	}
	root, ok := l.set.Set.Roots[targetID]
	if !ok || root.TargetID != targetID || root.Err != nil || !validObjectID(root.CommitHash) || !validObjectID(root.TreeHash) {
		return nil, ErrAdmissionUnavailable
	}
	dir, err := a.shadow.openShadowRepo(targetID)
	if err != nil {
		return nil, ErrAdmissionUnavailable
	}
	if !physicalDirectory(dir) {
		return nil, ErrAdmissionUnavailable
	}
	// Commit and tree must be the recorded pair, not independently supplied IDs.
	commit, err := boundedSnapshotGit(ctx, a.shadow, dir, 1<<20, "cat-file", "commit", root.CommitHash)
	if err != nil || gitCapturedHash("commit", commit, len(root.CommitHash)) != root.CommitHash || !bytes.HasPrefix(commit, []byte("tree "+root.TreeHash+"\n")) {
		return nil, ErrAdmissionUnavailable
	}
	tree, err := boundedSnapshotGit(ctx, a.shadow, dir, 128, "rev-parse", root.CommitHash+"^{tree}")
	if err != nil || strings.TrimSpace(string(tree)) != root.TreeHash {
		return nil, ErrAdmissionUnavailable
	}

	b := a.policy.Budgets
	if b.MaxCaptureEntries > 1<<20 {
		return nil, ErrSnapshotBudget
	}
	var files []CapturedFile
	var total int64
	entries := 0
	var walk func(string, string) error
	walk = func(oid, prefix string) error {
		if err := l.Verify(ctx); err != nil {
			return err
		}
		if err := a.guard.check(); err != nil {
			return err
		}
		raw, err := boundedSnapshotGit(ctx, a.shadow, dir, int64(b.MaxCaptureEntries)*4352, "cat-file", "tree", oid)
		if err != nil {
			return err
		}
		if gitCapturedHash("tree", raw, len(oid)) != oid {
			return ErrAdmissionUnavailable
		}
		hashBytes := len(oid) / 2
		for len(raw) > 0 {
			entries++
			if entries > b.MaxCaptureEntries {
				return ErrSnapshotBudget
			}
			space := bytes.IndexByte(raw, ' ')
			nul := bytes.IndexByte(raw, 0)
			if space <= 0 || nul <= space || len(raw)-nul-1 < hashBytes {
				return ErrAdmissionUnavailable
			}
			mode := string(raw[:space])
			name := string(raw[space+1 : nul])
			hash := hex.EncodeToString(raw[nul+1 : nul+1+hashBytes])
			raw = raw[nul+1+hashBytes:]
			if strings.Contains(name, "/") {
				return ErrAdmissionUnavailable
			}
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			if !safeCapturedPath(rel) || SecretExcluded(rel) {
				return ErrAdmissionUnavailable
			}
			if mode == "40000" {
				if err := walk(hash, rel); err != nil {
					return err
				}
				continue
			}
			fileMode := fs.FileMode(0600)
			switch mode {
			case "100644":
			case "100755":
				fileMode = 0700
			default:
				return ErrAdmissionUnavailable
			}
			sizeRaw, err := boundedSnapshotGit(ctx, a.shadow, dir, 128, "cat-file", "-s", hash)
			if err != nil {
				return err
			}
			size, err := strconv.ParseInt(strings.TrimSpace(string(sizeRaw)), 10, 64)
			if err != nil || size < 0 || size > b.MaxRootBytes-total || size > b.MaxCaptureBytes-total {
				return ErrSnapshotBudget
			}
			content, err := boundedSnapshotGit(ctx, a.shadow, dir, size, "cat-file", "blob", hash)
			if err != nil || int64(len(content)) != size || gitCapturedHash("blob", content, len(hash)) != hash {
				return ErrAdmissionUnavailable
			}
			total += size
			files = append(files, CapturedFile{Path: rel, Mode: fileMode, Bytes: append([]byte{}, content...)})
		}
		return nil
	}
	if err := walk(root.TreeHash, ""); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, ErrAdmissionUnavailable
	}
	return files, l.Verify(ctx)
}
func gitCapturedHash(kind string, data []byte, width int) string {
	if width == 40 && kind == "blob" {
		return gitBlobHash(data)
	}
	if width == 40 {
		h := sha1.New()
		fmt.Fprintf(h, "%s %d\x00", kind, len(data))
		h.Write(data)
		return hex.EncodeToString(h.Sum(nil))
	}
	if width == 64 {
		h := sha256.New()
		fmt.Fprintf(h, "%s %d\x00", kind, len(data))
		h.Write(data)
		return hex.EncodeToString(h.Sum(nil))
	}
	return ""
}

// ValidateForkDestination rejects aliases of captured roots or protected store
// paths under the current held read lease. It is a refusal check, not a grant.
func (l *ReadLease) ValidateForkDestination(ctx context.Context, destination string) error {
	if err := l.Verify(ctx); err != nil {
		return err
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || snapshotPathsOverlap(destination, l.admission.root) {
		return ErrAdmissionUnavailable
	}
	for _, root := range l.set.Set.Roots {
		if snapshotPathsOverlap(destination, root.Root) {
			return ErrAdmissionUnavailable
		}
	}
	return nil
}
func snapshotPathsOverlap(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return true
	}
	within := func(root, p string) bool {
		rel, e := filepath.Rel(root, p)
		return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}

func safeCapturedPath(rel string) bool {
	if rel == "" || len(rel) > 4096 || path.Clean(rel) != rel || path.IsAbs(rel) || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.ContainsAny(rel, "\x00\\") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".git" || part == "" {
			return false
		}
	}
	return true
}

type snapshotOutput struct {
	data     bytes.Buffer
	limit    int64
	exceeded bool
}

func (w *snapshotOutput) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-int64(w.data.Len()) {
		w.exceeded = true
		return 0, ErrSnapshotBudget
	}
	return w.data.Write(p)
}
func boundedSnapshotGit(ctx context.Context, sg *ShadowGit, gitDir string, limit int64, args ...string) ([]byte, error) {
	if limit < 0 {
		return nil, ErrSnapshotBudget
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := append([]string{"--git-dir=" + filepath.Clean(gitDir), "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, sg.gitBin, command...) //nolint:gosec // closed Git operations against the admitted store
	cmd.Dir = sg.baseDir
	cmd.Env = isolatedEnv([]string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1"})
	output := &snapshotOutput{limit: limit}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if output.exceeded {
		return nil, ErrSnapshotBudget
	}
	if err != nil {
		return nil, errors.Join(ErrAdmissionUnavailable, ctx.Err())
	}
	return output.data.Bytes(), nil
}
