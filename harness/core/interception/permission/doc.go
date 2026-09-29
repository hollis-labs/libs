// Package permission is an in-process authorization core for tool
// invocations: an Engine answers allow, deny or ask for a (session, tool,
// input) triple, and drives the approval round trip when the answer is ask.
//
// # Model
//
// A caller builds an Engine with NewEngine, calls Engine.Check before
// running a tool, and acts on the Decision. On DecisionAsk it surfaces the
// question to whoever decides (this package has no transport), using
// Engine.RequestApproval and Engine.WaitForApproval, while the deciding side
// calls Engine.Respond. Decisions come from a Mode, an ordered RuleSet (YAML
// loadable), and per-session grants recorded by approvals.
//
// # Precedence
//
// Check applies, in order: ModeYolo, ModePlan, session grants, deny rules,
// ask rules, allow rules, then the mode default. Two consequences are
// deliberate and pinned by tests: yolo mode beats deny rules, and session
// grants are keyed by tool name only, so "allow for session" on a tool also
// overrides later deny rules for that tool.
//
// # Sharp edges
//
// A rule Pattern is matched only against input values under the Matcher's
// recognised keys (path, file, directory, file_path, command by default);
// a tool with another input key silently never matches a rule that has a
// Pattern. Command matching is a plain substring test and is advisory, not a
// security boundary: an allow pattern "git" also matches "git; rm -rf ~".
// Use WithMatcher to widen the keys or replace the command test.
//
// # Auditing
//
// WithAuditor receives an Event for every decision and approval step. Events
// never carry tool input. SlogAuditor logs approval events through log/slog.
//
// The sub-packages permission/pathgrants (session-scoped path grants) and
// permission/summary (prompt text describing effective path access) build on
// this package; this package never imports them.
package permission
