package permission_test

import (
	"context"
	"fmt"
	"time"

	permission "github.com/hollis-labs/go-permission"
)

func ExampleEngine_Check() {
	rules := &permission.RuleSet{Rules: []permission.Rule{
		{Tool: "shell", Pattern: "rm -rf", Behavior: permission.DecisionDeny},
	}}
	e := permission.NewEngine(permission.ModeDefault, rules)

	res := e.Check(context.Background(), "session-1", "shell",
		map[string]any{"command": "rm -rf /tmp/x"}, permission.ToolMeta{IsDestructive: true})
	fmt.Println(res.Decision, "-", res.Reason)

	res = e.Check(context.Background(), "session-1", "shell",
		map[string]any{"command": "ls"}, permission.ToolMeta{IsDestructive: true})
	fmt.Println(res.Decision, "-", res.Reason)
	// Output:
	// deny - denied by rule: tool=shell pattern=rm -rf
	// ask - default mode — destructive operation requires approval
}

func ExampleEngine_RequestApproval() {
	e := permission.NewEngine(permission.ModeDefault, nil,
		permission.WithApprovalTimeout(time.Second))
	ctx := context.Background()

	req := e.RequestApproval("session-1", "shell", nil, "destructive operation")
	// The deciding side answers, usually from another goroutine.
	e.Respond(req.ID, permission.DecisionAllow, permission.ScopeSession, "session-1")
	resp := e.WaitForApproval(ctx, req)
	fmt.Println(resp.Decision, resp.Scope)

	// The session grant now short-circuits the ask.
	res := e.Check(ctx, "session-1", "shell", nil, permission.ToolMeta{IsDestructive: true})
	fmt.Println(res.Decision, "-", res.Reason)
	// Output:
	// allow session
	// allow - session grant
}

func ExampleRuleSet_Validate() {
	rs := &permission.RuleSet{Rules: []permission.Rule{
		{Tool: "shell", Behavior: "dney"}, // typo: would silently never fire
	}}
	fmt.Println(rs.Validate())
	// Output:
	// rule 0 (tool="shell"): unknown behavior "dney"
}

func ExampleWithFileEditTools() {
	e := permission.NewEngine(permission.ModeAcceptEdits, nil,
		permission.WithFileEditTools("write_file"))
	res := e.Check(context.Background(), "s", "write_file", nil, permission.ToolMeta{})
	fmt.Println(res.Decision)
	// Output:
	// allow
}

func ExampleWithMatcher() {
	rules := &permission.RuleSet{Rules: []permission.Rule{
		{Tool: "*", Pattern: "/secrets/**", Behavior: permission.DecisionDeny},
	}}
	// This tool passes its path under "target", which the defaults do not
	// recognise; extend the keys so the rule can see it.
	e := permission.NewEngine(permission.ModeDefault, rules,
		permission.WithMatcher(permission.Matcher{PathKeys: []string{"target"}}))
	res := e.Check(context.Background(), "s", "copy", map[string]any{"target": "/secrets/key"}, permission.ToolMeta{})
	fmt.Println(res.Decision)
	// Output:
	// deny
}
