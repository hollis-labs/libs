package reflexspec

import (
	"encoding/json"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Trigger kinds accepted in Definition.TriggerKind.
const (
	TriggerPredicate = "predicate"
	TriggerEvent     = "event"
	TriggerInterval  = "interval"
)

// TriggerKinds returns the trigger kinds, sorted. These are grammar, not a
// host catalog: each one selects a different trigger_spec shape.
func TriggerKinds() []string {
	return []string{TriggerEvent, TriggerInterval, TriggerPredicate}
}

func isTriggerKind(k string) bool {
	return k == TriggerPredicate || k == TriggerEvent || k == TriggerInterval
}

// Predicate combinators.
const (
	KindAND = "AND"
	KindOR  = "OR"
)

type valueType int

const (
	tInt valueType = iota
	tNumber
	tString
	tOp
	tEnum
	tRegex
	tNames
)

type fieldRule struct {
	name     string
	typ      valueType
	min      int64    // tInt: inclusive minimum
	oneOf    []string // tOp, tEnum
	required bool
}

var (
	numericOps = []string{"=", "==", "!=", "<", "<=", ">", ">="}
	stringOps  = []string{"=", "==", "!="}
	textScopes = []string{"user", "assistant", "all"}
	nameModes  = []string{"any", "none", "absent", "all"}
	entities   = []string{"task_id", "torque_task_id", "path", "file_path", "agent", "agent_id", "agent_urn", "url"}
)

func numericWindow() []fieldRule {
	return []fieldRule{
		{name: "window", typ: tInt, min: 1},
		{name: "op", typ: tOp, oneOf: numericOps},
		{name: "value", typ: tInt, min: math.MinInt64},
	}
}

func nameWindow() []fieldRule {
	return []fieldRule{
		{name: "window", typ: tInt, min: 1},
		{name: "mode", typ: tEnum, oneOf: nameModes},
		{name: "names", typ: tNames},
		{name: "pattern", typ: tRegex},
	}
}

// leafRules is the predicate grammar of go-reflexes v0.1.0
// (evaluator.go, evalPredicateNode) as data. It is the single source for
// PredicateKinds and for the walk. A field the engine would silently default
// or ignore is rejected when misspelled, because "never fires" is exactly the
// failure this package exists to surface.
var leafRules = map[string][]fieldRule{
	"tool_calls_window":       numericWindow(),
	"cache_read_window":       numericWindow(),
	"input_tokens_window":     numericWindow(),
	"output_growth_window":    {{name: "window", typ: tInt, min: 2}, {name: "factor", typ: tNumber}},
	"regex_match_window":      {{name: "window", typ: tInt, min: 1}, {name: "pattern", typ: tRegex, required: true}},
	"user_regex_window":       {{name: "window", typ: tInt, min: 1}, {name: "pattern", typ: tRegex, required: true}},
	"text_regex_window":       {{name: "window", typ: tInt, min: 1}, {name: "pattern", typ: tRegex, required: true}, {name: "scope", typ: tEnum, oneOf: textScopes}},
	"entity_mention_window":   {{name: "window", typ: tInt, min: 1}, {name: "entity", typ: tEnum, oneOf: entities}, {name: "pattern", typ: tRegex}, {name: "scope", typ: tEnum, oneOf: textScopes}},
	"tool_name_window":        nameWindow(),
	"envelope_type_window":    nameWindow(),
	"mail_unread_count":       {{name: "op", typ: tOp, oneOf: numericOps}, {name: "value", typ: tInt, min: math.MinInt64}},
	"identical_output_window": {{name: "window", typ: tInt, min: 1}},
	"prefix_pressure":         {{name: "ratio", typ: tNumber}, {name: "context_window", typ: tInt, min: 1}},
	"attr":                    {{name: "key", typ: tString, required: true}, {name: "value", typ: tString}, {name: "op", typ: tOp, oneOf: stringOps}},
}

// PredicateKinds returns every predicate node kind of the grammar: the
// AND and OR combinators and the leaf kinds, sorted.
func PredicateKinds() []string {
	out := []string{KindAND, KindOR}
	for k := range leafRules {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkTriggerSpec(kind, spec string, cfg *validateConfig) []error {
	if !isTriggerKind(kind) {
		if kind == "" {
			return nil // Validate reports the missing kind itself.
		}
		return []error{newErr("trigger_kind", CodeUnknownTriggerKind,
			"unknown trigger_kind %q (want predicate, event or interval)", kind)}
	}
	if strings.TrimSpace(spec) == "" {
		return []error{newErr("trigger_spec", CodeRequired, "trigger_spec is required")}
	}
	node, err := decodeObject("trigger_spec", spec)
	if err != nil {
		return []error{err}
	}
	w := &walker{cfg: cfg}
	switch kind {
	case TriggerPredicate:
		w.predicate("trigger_spec", node, 0)
	case TriggerEvent:
		w.event("trigger_spec", node)
	case TriggerInterval:
		w.interval("trigger_spec", node)
	}
	return w.errs
}

// decodeObject parses s as exactly one JSON object, keeping numbers exact.
func decodeObject(field, s string) (map[string]any, *Error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, newErr(field, CodeInvalidJSON, "invalid JSON: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, newErr(field, CodeInvalidJSON, "invalid JSON: unexpected data after the first value")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, newErr(field, CodeNotObject, "must be a JSON object")
	}
	return obj, nil
}

type walker struct {
	cfg  *validateConfig
	errs []error
}

func (w *walker) add(field, code, format string, args ...any) {
	w.errs = append(w.errs, newErr(field, code, format, args...))
}

func (w *walker) predicate(path string, node map[string]any, depth int) {
	if depth > MaxPredicateDepth {
		w.add(path, CodeTooDeep, "predicate nests deeper than %d levels", MaxPredicateDepth)
		return
	}
	raw, present := node["kind"]
	kind, isStr := raw.(string)
	switch {
	case !present:
		w.add(path+".kind", CodeRequired, "predicate node needs a kind")
		return
	case !isStr:
		w.add(path+".kind", CodeWrongType, "kind must be a string")
		return
	}
	if kind == KindAND || kind == KindOR {
		w.combinator(path, kind, node, depth)
		return
	}
	rules, ok := leafRules[kind]
	if !ok {
		w.add(path+".kind", CodeUnknownPredicateKind,
			"unknown predicate kind %q; the engine would treat it as never firing", kind)
		return
	}
	allowed := map[string]bool{"kind": true}
	for _, r := range rules {
		allowed[r.name] = true
		w.field(path, node, r)
	}
	w.unknownFields(path, node, allowed)
	w.crossChecks(path, kind, node)
}

func (w *walker) combinator(path, kind string, node map[string]any, depth int) {
	w.unknownFields(path, node, map[string]bool{"kind": true, "clauses": true})
	raw, present := node["clauses"]
	if !present {
		w.add(path+".clauses", CodeRequired, "%s needs a non-empty clauses array", kind)
		return
	}
	clauses, ok := raw.([]any)
	if !ok {
		w.add(path+".clauses", CodeWrongType, "clauses must be an array")
		return
	}
	if len(clauses) == 0 {
		w.add(path+".clauses", CodeRequired, "%s with no clauses never fires", kind)
		return
	}
	for i, c := range clauses {
		cp := path + ".clauses[" + strconv.Itoa(i) + "]"
		cm, ok := c.(map[string]any)
		if !ok {
			w.add(cp, CodeNotObject, "clause must be a JSON object")
			continue
		}
		w.predicate(cp, cm, depth+1)
	}
}

func (w *walker) unknownFields(path string, node map[string]any, allowed map[string]bool) {
	var extra []string
	for k := range node {
		if !allowed[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		w.add(path+"."+k, CodeUnknownField, "%q is not a field of this node kind; the engine would ignore it", k)
	}
}

func (w *walker) field(path string, node map[string]any, r fieldRule) {
	fp := path + "." + r.name
	raw, present := node[r.name]
	if !present {
		if r.required {
			w.add(fp, CodeRequired, "%s is required", r.name)
		}
		return
	}
	switch r.typ {
	case tInt:
		n, ok := asInt(raw)
		if !ok {
			w.add(fp, CodeWrongType, "%s must be an integer", r.name)
		} else if n < r.min {
			w.add(fp, CodeOutOfRange, "%s must be >= %d", r.name, r.min)
		}
	case tNumber:
		f, ok := asFloat(raw)
		if !ok {
			w.add(fp, CodeWrongType, "%s must be a number", r.name)
		} else if f <= 0 {
			w.add(fp, CodeOutOfRange, "%s must be > 0", r.name)
		}
	case tString:
		s, ok := raw.(string)
		if !ok {
			w.add(fp, CodeWrongType, "%s must be a string", r.name)
		} else if r.required && s == "" {
			w.add(fp, CodeRequired, "%s must not be empty", r.name)
		}
	case tOp, tEnum:
		s, ok := raw.(string)
		if !ok {
			w.add(fp, CodeWrongType, "%s must be a string", r.name)
		} else if s != "" && !contains(r.oneOf, s) {
			w.add(fp, CodeInvalidValue, "%s %q is not one of %s", r.name, s, strings.Join(r.oneOf, ", "))
		}
	case tRegex:
		s, ok := raw.(string)
		switch {
		case !ok:
			w.add(fp, CodeWrongType, "%s must be a string", r.name)
		case s == "" && r.required:
			w.add(fp, CodeRequired, "%s must not be empty", r.name)
		case s != "":
			if _, err := regexp.Compile(s); err != nil {
				w.add(fp, CodeInvalidRegex, "%s does not compile: %v", r.name, err)
			}
		}
	case tNames:
		switch v := raw.(type) {
		case string:
			// The engine also accepts a comma-separated string.
		case []any:
			for i, item := range v {
				if s, ok := item.(string); !ok || s == "" {
					w.add(fp+"["+strconv.Itoa(i)+"]", CodeWrongType, "names entries must be non-empty strings")
				}
			}
		default:
			w.add(fp, CodeWrongType, "names must be an array of strings")
		}
	}
}

// crossChecks covers requirements that span fields.
func (w *walker) crossChecks(path, kind string, node map[string]any) {
	switch kind {
	case "tool_name_window", "envelope_type_window":
		if !hasNames(node["names"]) && !nonEmptyString(node["pattern"]) {
			w.add(path+".names", CodeRequired, "%s needs names or pattern", kind)
		}
	case "entity_mention_window":
		if !nonEmptyString(node["pattern"]) && !nonEmptyString(node["entity"]) {
			w.add(path+".entity", CodeRequired, "entity_mention_window needs entity or pattern")
		}
	}
}

func (w *walker) event(path string, node map[string]any) {
	w.unknownFields(path, node, map[string]bool{"name": true})
	raw, present := node["name"]
	name, ok := raw.(string)
	switch {
	case !present || (ok && name == ""):
		w.add(path+".name", CodeRequired, "event trigger needs a non-empty name")
	case !ok:
		w.add(path+".name", CodeWrongType, "name must be a string")
	case strings.IndexFunc(name, isSpaceOrControl) >= 0:
		w.add(path+".name", CodeInvalidValue, "event name %q must not contain whitespace or control characters", name)
	case w.cfg.knownEvents != nil && !w.cfg.knownEvents(name):
		w.add(path+".name", CodeNotInCatalog, "event name %q is not in the configured catalog", name)
	}
}

func (w *walker) interval(path string, node map[string]any) {
	w.unknownFields(path, node, map[string]bool{"every_n_ticks": true})
	raw, present := node["every_n_ticks"]
	if !present {
		w.add(path+".every_n_ticks", CodeRequired, "interval trigger needs every_n_ticks")
		return
	}
	n, ok := asInt(raw)
	switch {
	case !ok:
		w.add(path+".every_n_ticks", CodeWrongType, "every_n_ticks must be an integer")
	case n < 1:
		w.add(path+".every_n_ticks", CodeOutOfRange, "every_n_ticks must be >= 1; the engine never fires at 0 or below")
	}
}

func isSpaceOrControl(r rune) bool {
	return r <= ' ' || r == 0x7f || r == 0x85 || r == 0xa0 || r == 0x2028 || r == 0x2029
}

func hasNames(raw any) bool {
	switch v := raw.(type) {
	case string:
		for _, p := range strings.Split(v, ",") {
			if strings.TrimSpace(p) != "" {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				return true
			}
		}
	}
	return false
}

func nonEmptyString(raw any) bool {
	s, ok := raw.(string)
	return ok && s != ""
}

func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

// asInt accepts a JSON number with an integral value (so 3 and 3.0), the
// way the engine's float64-to-int conversion does, but not 3.5.
func asInt(raw any) (int64, bool) {
	n, ok := raw.(json.Number)
	if !ok {
		return 0, false
	}
	if i, err := n.Int64(); err == nil {
		return i, true
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

func asFloat(raw any) (float64, bool) {
	n, ok := raw.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}
