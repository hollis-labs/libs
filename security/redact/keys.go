package redact

import (
	"strings"
	"unicode"
)

// A key (an environment variable, a JSON field, a CLI flag, a query parameter) is
// sensitive when one of its words says so. Words are split on every non-alphanumeric
// character and on camelCase boundaries, so API_KEY, apiKey, api-key and "X-API-Key"
// are one spelling. Matching words rather than substrings is what keeps "tokenizer",
// "author", "keyboard" and "passport" alone.

// sensitiveWords make a key sensitive on their own. A word also counts when it ENDS
// with one of the longer ones (PGPASSWORD, DBPASSWORD, MYSECRET), which is how
// environment variables are often written without a separator.
var sensitiveWords = set(
	"token", "secret", "password", "passwd", "passphrase", "passcode", "credential", "credentials",
	"cookie", "authorization", "bearer", "apikey", "privatekey", "accesskey", "secretkey", "signingkey", "masterkey",
)

var sensitiveSuffixes = []string{"password", "passwd", "passphrase", "secret", "token", "apikey"}

// sensitivePairs are two adjacent words that are sensitive together ("api" "key").
var sensitivePairs = map[[2]string]bool{
	{"api", "key"}: true, {"private", "key"}: true, {"access", "key"}: true, {"signing", "key"}: true,
	{"master", "key"}: true, {"encryption", "key"}: true, {"auth", "key"}: true, {"secret", "key"}: true,
	{"mysql", "pwd"}: true, {"client", "secret"}: true,
}

// benignLast words end a key that merely describes a secret instead of holding one:
// PASSWORD_MIN_LENGTH, token_type, secret_name, credential_missing, SECRET_ROTATION_DAYS.
var benignLast = set(
	"length", "len", "min", "max", "count", "limit", "limits", "ttl", "ms", "sec", "secs", "seconds", "mins", "minutes",
	"hours", "days", "timeout", "expires", "expiry", "expiration", "expired", "type", "name", "names", "file", "path",
	"ref", "refs", "reference", "env", "var", "missing", "required", "error", "errors", "status", "state", "mode",
	"enabled", "disabled", "kind", "id", "ids", "url", "uri", "endpoint", "policy", "rotation", "stdin", "prompt",
	"header", "headers", "field", "fields", "label", "labels", "description", "desc", "help", "hint", "regex", "pattern",
	"source", "provider", "scope", "scopes", "bucket", "usage", "used", "cost", "price", "version", "format", "alg",
	"algorithm", "hash", "digest", "fingerprint", "location", "dir", "directory", "store", "manager", "helper",
)

// Plural token/secret/key words are counts or lists of names (max_tokens,
// missing_secrets), not values.
func benignWord(w string) bool {
	switch w {
	case "tokens", "secrets", "passwords", "keys", "cookies", "tokenizer", "tokenize", "tokenized":
		return true
	}
	return false
}

// segments splits a key into lower-case words.
func segments(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r):
			// boundary before an upper-case letter that follows a lower-case letter or
			// digit (apiKey), or that starts a word after an acronym (APIKey -> API Key)
			if len(cur) > 0 {
				prev := rs[i-1]
				nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					flush()
				}
			}
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// keyPolicy decides whether a key names a secret.
type keyPolicy struct {
	extraSensitive map[string]bool // normalized whole keys
	extraBenign    map[string]bool
}

func normKey(s string) string { return strings.Join(segments(s), "_") }

// sensitive reports whether name holds a secret value.
func (p *keyPolicy) sensitive(name string) bool {
	segs := segments(name)
	if len(segs) == 0 {
		return false
	}
	norm := strings.Join(segs, "_")
	if p != nil {
		if p.extraBenign[norm] {
			return false
		}
		if p.extraSensitive[norm] {
			return true
		}
	}
	if benignLast[segs[len(segs)-1]] {
		return false
	}
	hit := false
	for i, w := range segs {
		if benignWord(w) {
			continue
		}
		if sensitiveWords[w] {
			hit = true
		} else {
			for _, suf := range sensitiveSuffixes {
				if len(w) > len(suf) && strings.HasSuffix(w, suf) {
					hit = true
				}
			}
		}
		if i+1 < len(segs) && sensitivePairs[[2]string{w, segs[i+1]}] {
			hit = true
		}
	}
	return hit
}

// SensitiveName reports whether an environment variable, field, flag or query
// parameter name holds a secret. It is the one predicate for "does this name
// hold a secret", so an application that scrubs environments and one that
// redacts logs agree.
func SensitiveName(name string) bool { return (*keyPolicy)(nil).sensitive(name) }
