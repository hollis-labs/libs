package permission

import "fmt"

// ProfileBindingVersion is launch evidence. Change it when binding semantics
// change; the definition pins only the name. These bindings grant no mesh verbs.
const ProfileBindingVersion = "harness-permission-profiles-v1"

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

type ProfileError struct {
	Code   string
	Name   string
	Reason string
}

func (e *ProfileError) Error() string {
	return fmt.Sprintf("%s: profile=%q: %s", e.Code, e.Name, e.Reason)
}

// BindProfile does exact authored lookup, never a string cast or ambient default.
// authorizedModes must already be the intersection of definition, assignment
// and host ceilings. An empty intersection refuses every profile. Missing names
// require an explicit host default at the caller, not an implicit fallback here.
func BindProfile(name string, authorizedModes []Mode) (ProfileBinding, error) {
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
		return ProfileBinding{}, &ProfileError{"unknown_permission_profile", name, "no authored binding; missing names need an explicit host default"}
	}
	for _, allowed := range authorizedModes {
		if allowed == binding.Mode {
			return binding, nil
		}
	}
	return ProfileBinding{}, &ProfileError{"permission_ceiling", name, "mode is outside authorized ceilings (yolo bypasses deny rules)"}
}
