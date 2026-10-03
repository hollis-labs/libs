package worktree

import "strings"

// rawEntry is one record of `git worktree list --porcelain`.
type rawEntry struct {
	Path     string
	HEAD     string
	Branch   string // short name; "" when detached or bare
	Detached bool
	Bare     bool
	Locked   bool
	Prunable bool
}

// parsePorcelain parses the output of `git worktree list --porcelain`.
// Records are separated by blank lines; unknown attribute lines are ignored so
// newer git versions do not break the parser. The attributes `locked` and
// `prunable` may carry a reason after a space.
func parsePorcelain(out string) []rawEntry {
	var entries []rawEntry
	var cur *rawEntry
	flush := func() {
		if cur != nil && cur.Path != "" {
			entries = append(entries, *cur)
		}
		cur = nil
	}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		if key == "worktree" {
			flush()
			cur = &rawEntry{Path: val}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			cur.HEAD = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return entries
}

func trimNL(s string) string { return strings.TrimSpace(s) }
