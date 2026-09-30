package reflexspec

import (
	"strings"
)

// The seven action kinds go-reflexes and Nanite know about.
const (
	ActionInjectReminder  = "inject_reminder"
	ActionForceToolChoice = "force_tool_choice"
	ActionDispatchToAgent = "dispatch_to_agent"
	ActionHaltSession     = "halt_session"
	ActionAddSchedule     = "add_schedule"
	ActionSendMessage     = "send_message"
	ActionResumeLoopRun   = "resume_loop_run"
)

// DocumentedActionKinds returns the seven action kinds above, sorted. It is a
// convenience for WithKnownActionKinds, not a closed set: by default any
// well-formed kind is accepted.
func DocumentedActionKinds() []string {
	return []string{
		ActionAddSchedule, ActionDispatchToAgent, ActionForceToolChoice,
		ActionHaltSession, ActionInjectReminder, ActionResumeLoopRun, ActionSendMessage,
	}
}

func checkActionSpec(kind, spec string) []error {
	if strings.TrimSpace(spec) == "" {
		return []error{newErr("action_spec", CodeRequired, "action_spec is required")}
	}
	obj, err := decodeObject("action_spec", spec)
	if err != nil {
		return []error{err}
	}
	w := &walker{}
	switch kind {
	case ActionInjectReminder:
		w.requiredString("action_spec", obj, "body")
		w.optionalIdent("action_spec", obj, "urgency")
	case ActionHaltSession:
		w.optionalString("action_spec", obj, "reason")
	case ActionDispatchToAgent:
		w.requiredString("action_spec", obj, "agent_slug")
		w.optionalString("action_spec", obj, "reason")
		if raw, ok := obj["confidence"]; ok {
			f, isNum := asFloat(raw)
			switch {
			case !isNum:
				w.add("action_spec.confidence", CodeWrongType, "confidence must be a number")
			case f < 0 || f > 1:
				w.add("action_spec.confidence", CodeOutOfRange, "confidence must be within [0, 1]")
			}
		}
	case ActionResumeLoopRun:
		w.requiredString("action_spec", obj, "loop_run_id")
	}
	// force_tool_choice, add_schedule, send_message and unknown kinds: the
	// object check above is all there is, by ruling. No invented fields.
	return w.errs
}

func (w *walker) requiredString(path string, obj map[string]any, key string) {
	raw, ok := obj[key]
	if !ok {
		w.add(path+"."+key, CodeRequired, "%s is required", key)
		return
	}
	s, isStr := raw.(string)
	switch {
	case !isStr:
		w.add(path+"."+key, CodeWrongType, "%s must be a string", key)
	case strings.TrimSpace(s) == "":
		w.add(path+"."+key, CodeRequired, "%s must not be empty", key)
	}
}

func (w *walker) optionalString(path string, obj map[string]any, key string) {
	if raw, ok := obj[key]; ok {
		if _, isStr := raw.(string); !isStr {
			w.add(path+"."+key, CodeWrongType, "%s must be a string", key)
		}
	}
}

// optionalIdent checks an open enum by shape only. urgency was observed as
// info or warn in every seed, but nothing in either code base consumes it,
// so no closed set is enforced.
func (w *walker) optionalIdent(path string, obj map[string]any, key string) {
	raw, ok := obj[key]
	if !ok {
		return
	}
	s, isStr := raw.(string)
	switch {
	case !isStr:
		w.add(path+"."+key, CodeWrongType, "%s must be a string", key)
	case !identPattern.MatchString(s):
		w.add(path+"."+key, CodeInvalidValue, "%s %q must be lowercase words joined by _ . or -", key, s)
	}
}
