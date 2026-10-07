package host

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hollis-labs/plugin-sdk/capability"
)

func refusal(code capability.Code, name string) *capability.Error {
	return &capability.Error{Code: code, Capability: name, EffectState: capability.NotStarted}
}
func identifier(v string) bool {
	return utf8.ValidString(v) && v != "" && v != "*" && strings.TrimSpace(v) == v && !strings.ContainsFunc(v, unicode.IsControl)
}
func validNames(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if !identifier(v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func capabilityName(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) < 2 || ((parts[0] == "host" || parts[0] == "plugin") && len(parts) < 3) {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return false
			}
		}
	}
	return true
}
