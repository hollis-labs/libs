package hooks

import (
	"path"
	"unicode/utf8"
)

// MatchesTool reports whether a hook's Matcher pattern matches a tool name.
// The empty pattern, "*" and "**" match everything; anything else is a glob
// as understood by path.Match. A malformed pattern matches nothing.
//
// The glob syntax follows go-permission's matcher conventions for cross-lib
// consistency. It is NOT Claude Code's matcher grammar (exact names, "|" or
// "," lists, otherwise a regular expression); a host that forwards Matcher
// to a native Claude or Codex runtime must translate.
func MatchesTool(matcher, toolName string) bool {
	if matcher == "" || matcher == "*" || matcher == "**" {
		return true
	}
	ok, err := path.Match(matcher, toolName)
	if err != nil {
		return false
	}
	return ok
}

// TruncateContext trims s to at most limit bytes without splitting a UTF-8
// sequence. limit <= 0 means no trim. It is a helper only: nothing in this
// module calls it automatically, so a host can choose to reject an oversize
// AdditionalContext instead of silently losing data.
func TruncateContext(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
