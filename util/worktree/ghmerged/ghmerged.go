package ghmerged

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	worktree "github.com/hollis-labs/go-worktree"
)

// ErrUnavailable is wrapped when the gh binary cannot be found.
var ErrUnavailable = errors.New("gh is not available")

// DefaultLimit is the number of merged pull requests requested per call.
const DefaultLimit = 200

// Source lists merged pull requests through `gh`.
type Source struct {
	bin   string
	limit int
}

var _ worktree.MergedSource = (*Source)(nil)

// Option configures a [Source].
type Option func(*Source)

// WithBinary sets the gh executable (default "gh", looked up on PATH).
func WithBinary(bin string) Option { return func(s *Source) { s.bin = bin } }

// WithLimit sets how many merged pull requests to request (default
// [DefaultLimit]). Values <= 0 are ignored.
func WithLimit(n int) Option {
	return func(s *Source) {
		if n > 0 {
			s.limit = n
		}
	}
}

// New returns a Source.
func New(opts ...Option) *Source {
	s := &Source{bin: "gh", limit: DefaultLimit}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Merged runs gh in repoRoot, which lets gh infer the repository from that
// directory's remote.
func (s *Source) Merged(ctx context.Context, repoRoot string) ([]worktree.MergedRef, error) {
	bin, err := exec.LookPath(s.bin)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	cmd := exec.CommandContext(ctx, bin, "pr", "list", //nolint:gosec // bin is caller-configured, args are constant
		"--state", "merged",
		"--json", "headRefName,headRefOid",
		"--limit", strconv.Itoa(s.limit))
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh pr list: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return parse(stdout.Bytes())
}

func parse(data []byte) ([]worktree.MergedRef, error) {
	var rows []struct {
		HeadRefName string `json:"headRefName"`
		HeadRefOid  string `json:"headRefOid"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	out := make([]worktree.MergedRef, 0, len(rows))
	for _, r := range rows {
		if r.HeadRefName != "" {
			out = append(out, worktree.MergedRef{HeadRef: r.HeadRefName, HeadOID: r.HeadRefOid})
		}
	}
	return out, nil
}
