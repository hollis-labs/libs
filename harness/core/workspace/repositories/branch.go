// Package repositories applies authorized repository policies through host ports.
// It performs no ambient discovery, network operation, publication or launch.
package repositories

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var errBranch = errors.New("repository branch refused")

type BranchVariables struct{ Agent, Instance, Assignment, Repository string }

// ResolveBranch evaluates only fixed named variables, once, without a shell.
// Callers retain the concrete result in the request and receipt for resume.
func ResolveBranch(template string, v BranchVariables) (string, error) {
	values := map[string]string{"agent": v.Agent, "instance": v.Instance, "assignment": v.Assignment, "repository": v.Repository}
	var out strings.Builder
	for len(template) > 0 {
		i := strings.Index(template, "${")
		if i < 0 {
			out.WriteString(template)
			break
		}
		out.WriteString(template[:i])
		template = template[i+2:]
		end := strings.IndexByte(template, '}')
		if end < 0 {
			return "", errBranch
		}
		value, ok := values[template[:end]]
		if !ok || value == "" || strings.ContainsAny(value, "${}") {
			return "", errBranch
		}
		out.WriteString(value)
		template = template[end+1:]
	}
	branch := out.String()
	if !ValidBranch(branch) {
		return "", errBranch
	}
	return branch, nil
}

// ValidBranch rejects Git's unsafe branch forms; the port additionally checks
// local Git ref rules and branch/worktree collisions under the declared locks.
func ValidBranch(b string) bool {
	if b == "" || b == "@" || !utf8.ValidString(b) || strings.HasPrefix(b, "-") || strings.HasSuffix(b, ".") || strings.Contains(b, "..") || strings.Contains(b, "@{") || strings.ContainsAny(b, "~^:?*[\\${}") {
		return false
	}
	for _, r := range b {
		if r <= 32 || r == 127 {
			return false
		}
	}
	for _, part := range strings.Split(b, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
