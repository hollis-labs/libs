package hooks

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MatchesTool reports whether a hook's Matcher pattern matches a tool name,
// using Claude Code's three-way rule:
//
//   - "", "*" (and "**") match every tool.
//   - A pattern made only of letters, digits, '_', '-', spaces, ',' and '|'
//     is an exact tool name, or a list of exact names separated by '|' or
//     ',' ("Edit|Write", "Edit, Write"). Names are compared exactly and
//     case-sensitively.
//   - Anything else is an UNANCHORED regular expression ("^Notebook",
//     "mcp__memory__.*").
//
// It is not a glob: "mcp__memory__*" is a regular expression (the final '*'
// repeats '_'), not a wildcard suffix; write "mcp__memory__.*".
//
// Regular expressions are Go RE2, whereas Claude Code evaluates JavaScript
// regexes; the two agree for the simple patterns matchers use, but lookahead
// and backreferences are JS-only and do not compile here. A pattern that
// does not compile matches nothing.
func MatchesTool(matcher, toolName string) bool {
	if matcher == "" || matcher == "*" || matcher == "**" {
		return true
	}
	if isNameList(matcher) {
		for _, n := range strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' }) {
			if strings.TrimSpace(n) == toolName {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return false
	}
	return re.MatchString(toolName)
}

// isNameList reports whether s contains only characters that make it an
// exact-name list rather than a regular expression.
func isNameList(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == ' ', r == ',', r == '|':
		default:
			return false
		}
	}
	return true
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
