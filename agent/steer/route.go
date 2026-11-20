package steer

import (
	"regexp"
	"slices"
	"strings"

	"github.com/hollis-labs/libs/core/util/router"
)

var slashRe = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)\s*(.*)$`)

// MessageSubject is the routing subject for an owner's message: the fields a routing rule
// (libs core/util/router) matches on.
//
//	text    the message
//	slash   the command word after a leading "/" (letters, digits, "_", ".", "-"),
//	        lower-cased; a "/" followed by anything else is not a slash command
//	arg     the rest of the message after the slash word
//	view, module   of the window in front
//	slot    the primary slot
//	mode, layout, source
//
// Empty fields are left out, so an exists condition means "set".
func MessageSubject(id string, sit Situation, slot string) router.Subject {
	f := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			f[k] = v
		}
	}
	text := strings.TrimSpace(sit.Input)
	put("text", text)
	if m := slashRe.FindStringSubmatch(text); m != nil {
		put("slash", strings.ToLower(m[1]))
		put("arg", strings.TrimSpace(m[2]))
	}
	if sit.Focused != nil {
		put("view", sit.Focused.View)
		put("module", sit.Focused.Module)
	}
	put("slot", slot)
	put("mode", sit.Mode)
	put("layout", sit.Layout)
	put("source", sit.Source)
	return router.Subject{ID: id, Fields: f}
}

// ActiveRules is a rule set with the preset's rules switched on: a rule the preset names by
// id runs even if it is disabled. The other rules are unchanged. The input is not modified.
func ActiveRules(rs router.RuleSet, p Preset) router.RuleSet {
	out := rs
	out.Rules = slices.Clone(rs.Rules)
	for i, r := range out.Rules {
		if r.Disabled && slices.Contains(p.Rules, r.ID) {
			out.Rules[i].Disabled = false
		}
	}
	return out
}
