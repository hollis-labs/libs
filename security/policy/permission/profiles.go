package permission

import "fmt"

// ProfileBindingVersion is launch evidence. Change it when binding semantics
// change; the definition pins only the name. These bindings grant no mesh verbs.
const ProfileBindingVersion = "harness-permission-profiles-v1"

// ProfileBinding is the resolved named posture and its launch-evidence version.
type ProfileBinding struct {
	Name           string
	Mode           Mode
	Version        string
	SkipsDenyRules bool
}

var profileBindings = []ProfileBinding{
	{"default", ModeDefault, ProfileBindingVersion, false},
	{"plan", ModePlan, ProfileBindingVersion, false},
	{"accept-edits", ModeAcceptEdits, ProfileBindingVersion, false},
	{"yolo", ModeYolo, ProfileBindingVersion, true},
}

func ProfileBindings() []ProfileBinding { return append([]ProfileBinding(nil), profileBindings...) }

// ProfileError is a preparation refusal with a stable machine-readable code.
type ProfileError struct {
	Code   string
	Name   string
	Reason string
}

func (e *ProfileError) Error() string {
	return fmt.Sprintf("%s: profile=%q: %s", e.Code, e.Name, e.Reason)
}

// Stable refusal codes let callers distinguish host-default resolution from denial.
const (
	CodeMissingProfile    = "missing_permission_profile"
	CodeUnknownProfile    = "unknown_permission_profile"
	CodePermissionCeiling = "permission_ceiling"
	CodeDenyEnforcement   = "deny_enforcement_required"
)

// Ceiling is the already-authorized mode intersection and enforcement obligations.
// It neither computes mesh authority nor proves provider-native enforcement.
type Ceiling struct {
	Modes                   []Mode
	RequiresDenyEnforcement bool
}

// BindProfile does exact authored lookup, never a string cast or ambient default.
// Ceiling.Modes must already be the intersection of definition, assignment
// and host ceilings. Required deny enforcement excludes yolo even when its mode
// appears in that intersection. Provider-native enforcement is checked separately.
// Custom named profile registration remains deferred. An empty intersection
// refuses every profile. Missing names
// require an explicit host default at the caller, not an implicit fallback here.
func BindProfile(name string, ceiling Ceiling) (ProfileBinding, error) {
	if name == "" {
		return ProfileBinding{}, &ProfileError{CodeMissingProfile, name, "missing reference requires an explicit host default"}
	}
	var binding ProfileBinding
	found := false
	for _, b := range profileBindings {
		if b.Name == name {
			binding = b
			found = true
			break
		}
	}
	if !found {
		return ProfileBinding{}, &ProfileError{CodeUnknownProfile, name, "no authored binding; missing names need an explicit host default"}
	}
	if binding.SkipsDenyRules && ceiling.RequiresDenyEnforcement {
		return ProfileBinding{}, &ProfileError{CodeDenyEnforcement, name, "yolo bypasses required deny rules"}
	}
	for _, allowed := range ceiling.Modes {
		if allowed == binding.Mode {
			return binding, nil
		}
	}
	return ProfileBinding{}, &ProfileError{CodePermissionCeiling, name, "mode is outside authorized ceilings (yolo bypasses deny rules)"}
}
