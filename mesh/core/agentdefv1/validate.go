package agentdef

import (
	"fmt"
	"strings"
)

type validateConfig struct {
	known func(string) bool
}

// ValidateOption configures Validate; the zero value does pattern-only checks
// with no external capability catalog.
type ValidateOption func(*validateConfig)

// WithCapabilities supplies the known-capability check for requires/uses,
// typically backed by a shared capability vocabulary. Omitted, requires/uses
// are pattern-checked only.
func WithCapabilities(known func(name string) bool) ValidateOption {
	return func(c *validateConfig) { c.known = known }
}

// maxSkillNameLen is the Agent Skills name length limit.
const maxSkillNameLen = 64

// Validate applies the v1 field rules: required name/description, name
// pattern, identity enum, requires/uses pattern (plus the capability catalog
// if supplied), hooks and skills name pattern. It returns nil or a
// *ValidationError. It never validates tools (a free-form request) and does
// not check that skills exist on disk — that is ResolveSkills.
func (d *Definition) Validate(opts ...ValidateOption) error {
	var cfg validateConfig
	for _, o := range opts {
		o(&cfg)
	}
	var ve ValidationError
	add := func(field, format string, args ...any) {
		ve.Errors = append(ve.Errors, FieldError{field, fmt.Sprintf(format, args...)})
	}

	switch {
	case d.Name == "":
		add("name", "is required")
	case !namePattern.MatchString(d.Name):
		add("name", "%q must match ^[a-z0-9]+(-[a-z0-9]+)*$", d.Name)
	}
	if strings.TrimSpace(d.Description) == "" {
		add("description", "is required")
	}
	if d.Identity != "" && d.Identity != IdentityStable {
		add("identity", "%q is not valid; the only legal value is %q (or omit it)", d.Identity, IdentityStable)
	}
	for i, s := range d.Skills {
		switch {
		case !namePattern.MatchString(s):
			add(fmt.Sprintf("skills[%d]", i), "%q must match ^[a-z0-9]+(-[a-z0-9]+)*$", s)
		case len(s) > maxSkillNameLen:
			add(fmt.Sprintf("skills[%d]", i), "%q is longer than %d characters", s, maxSkillNameLen)
		}
	}
	for _, f := range []struct {
		field  string
		names  []string
		lookup bool
	}{
		{"requires", d.Requires, true},
		{"uses", d.Uses, true},
		{"hooks", d.Hooks, false},
	} {
		for i, n := range f.names {
			field := fmt.Sprintf("%s[%d]", f.field, i)
			switch {
			case !namePattern.MatchString(n):
				add(field, "%q must match ^[a-z0-9]+(-[a-z0-9]+)*$", n)
			case f.lookup && cfg.known != nil && !cfg.known(n):
				add(field, "unknown capability %q", n)
			}
		}
	}

	if len(ve.Errors) > 0 {
		return &ve
	}
	return nil
}
