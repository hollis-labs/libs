package host

import cap "github.com/hollis-labs/plugin-sdk/capability"

func refusal(code cap.Code, name string) *cap.Error {
	return &cap.Error{Code: code, Capability: name, EffectState: cap.NotStarted}
}
func validNames(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" || v == "*" || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
