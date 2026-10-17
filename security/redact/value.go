package redact

import (
	"strings"
)

// parseValue reads the value that starts at t[i] (after a separator and any
// spaces). It returns the byte range to redact: the inside of a quoted value, or
// an unquoted token. ok is false when there is no scalar value there (end of
// text, or the start of an object or array, which structured redaction handles).
func parseValue(t string, i int) (start, end int, ok bool) {
	if i >= len(t) {
		return 0, 0, false
	}
	// a quote written inside a JSON string: \"value\"
	if strings.HasPrefix(t[i:], `\"`) {
		if j := strings.Index(t[i+2:], `\"`); j >= 0 && !strings.ContainsAny(t[i+2:i+2+j], "\n") {
			return i + 2, i + 2 + j, j > 0
		}
		return 0, 0, false
	}
	switch c := t[i]; c {
	case '"', '\'':
		j := i + 1
		for j < len(t) && t[j] != '\n' {
			if t[j] == '\\' && j+1 < len(t) {
				j += 2
				continue
			}
			if t[j] == c {
				break
			}
			j++
		}
		if j > len(t) {
			j = len(t)
		}
		return i + 1, j, j > i+1
	case '[', '{', '\n', '\r':
		return 0, 0, false
	}
	j := i
	for j < len(t) && !isTerminator(t[j]) {
		j++
	}
	for j > i && (t[j-1] == '}' || t[j-1] == ']' || t[j-1] == ')') {
		j--
	}
	return i, j, j > i
}

func isTerminator(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', ',', ';', '&', '<', '>', '"', '\'':
		return true
	}
	return false
}

var placeholderWords = set(
	"true", "false", "null", "nil", "none", "undefined", "required", "optional", "string", "str", "int", "integer",
	"bool", "boolean", "empty", "unset", "masked", "hidden", "omitted", "redacted", "n/a", "na", "tbd", "todo", "-", "--",
	"yes", "no", "on", "off", "default", "token", "password", "secret", "value", "your_key", "your_token", "your-api-key",
)

// isPlaceholder reports a value that is not a secret: nothing, a type name, a
// boolean, an already-redacted marker, an unfilled template, a variable
// reference, or a reference to a secret held elsewhere (op://..., keychain://...).
func (r *Redactor) isPlaceholder(v string) bool {
	if v == "" || v == r.marker {
		return true
	}
	lv := strings.ToLower(v)
	if placeholderWords[lv] {
		return true
	}
	switch v[0] {
	case '<', '$', '%':
		return true
	case '{':
		return strings.HasPrefix(v, "{{")
	}
	if strings.HasPrefix(lv, "[redacted") || strings.HasPrefix(lv, "(redacted") {
		return true
	}
	if strings.Trim(v, "*•x.X#_-") == "" && len(v) >= 3 { // ****, xxxx, ....
		return true
	}
	for _, s := range r.refs {
		if strings.HasPrefix(lv, s) {
			return true
		}
	}
	return false
}

// looksLikeToken reports whether text that follows the word "Bearer" is plausibly
// a credential and not an ordinary word. "accepts a Bearer JWT only" and "use
// Bearer auth" are prose; a credential is long or contains more than letters.
func looksLikeToken(v string) bool {
	if len(v) >= 20 {
		return true
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// messageLike reports a value that starts like a diagnostic, used to tell
// "AzureCLICredential: ERROR: AADSTS..." (a Go type name and an error) from
// "ApiKey: abc123".
func messageLike(v string) bool {
	lv := strings.ToLower(v)
	for _, w := range []string{"error", "failed", "failure", "warning", "warn", "fatal", "denied", "timeout", "unable"} {
		if strings.HasPrefix(lv, w) {
			return true
		}
	}
	return false
}

// isTypeName reports a CamelCase identifier with an internal capital, the shape
// of a Go type or class name.
func isTypeName(k string) bool {
	if k == "" || k[0] < 'A' || k[0] > 'Z' {
		return false
	}
	lower, upper := false, false
	for i := 1; i < len(k); i++ {
		switch {
		case k[i] >= 'a' && k[i] <= 'z':
			lower = true
		case k[i] >= 'A' && k[i] <= 'Z' && lower:
			upper = true
		case k[i] == '_' || k[i] == '-' || k[i] == '.':
			return false
		}
	}
	return lower && upper
}

// PII key values may be quoted across lines (for example an address). The
// credential-only parser keeps its historical line boundary semantics.
func (r *Redactor) parseKeyValue(t string, i int) (int, int, bool) {
	if r.policy.piiOnly == 0 || i >= len(t) {
		return parseValue(t, i)
	}
	if strings.HasPrefix(t[i:], `\"`) {
		if n := strings.Index(t[i+2:], `\"`); n >= 0 {
			return i + 2, i + 2 + n, n > 0
		}
		return i + 2, len(t), len(t) > i+2
	}
	if t[i] != '"' && t[i] != '\'' {
		return parseValue(t, i)
	}
	end := i + 1
	for end < len(t) {
		if t[end] == '\\' && end+1 < len(t) {
			end += 2
			continue
		}
		if t[end] == t[i] {
			break
		}
		end++
	}
	return i + 1, end, end > i+1
}
