package toolresult_test

import (
	"encoding/json"
	"strings"
)

// goldenCase is one fixture for the parity test against Nanite's output. The
// same file is compiled into the one-off differential harness that produced
// testdata/golden (see testdata/golden/README.txt).
type goldenCase struct {
	name    string
	body    string
	budget  int
	hardCap int
	fetch   []map[string]any
	search  []map[string]any
}

const torqueDescription = `## What
The core of A2A protocol adoption (CW-20260813-0002). Implements the design doc's "Task lifecycle and routing" and "Persistence" sections — this is the piece that makes the "A2A is a protocol adapter, not a new execution substrate" principle real: every Task is fulfilled by exactly one of Nanite's two existing execution paths, never a third parallel mechanism.

## How to fix
- New a2a_tasks table (+ migration): task ID, target kind (workflow | instance), target reference, caller-supplied message, durable_agent_instance_id (set once routing resolves), derived TaskState (cached, refreshed on read and on state-change triggers), push notification config (nullable, populated by the later push-notifications ticket), created/updated timestamps. This table is bookkeeping and translation only — it points at workflow_runs/durable_agent_instances, it does not duplicate their state.
- TaskManager service implementation in internal/service (plain functions/methods, thin-wrapper-over-service-layer pattern, per internal/a2a's pure wire types from CW-20260814-0014). Two routing outcomes on submission:
  - Target = workflow skill: call the existing WorkflowLauncher.Launch (Agent Workflows pillar) to start a new template-class durable-agent instance. Store the resulting durable_agent_instance_id.
  - Target = existing instance address: call DurableWake.Wake() with Reason: DurableAgentWakeExternalMessage (making this enum value real for the first time) and WakePayload.Prompt set from the Task's message. This requires CW-20260814-0013 (Prompt injection fix) to have landed.
- TaskState must be derived from the real execution status being tracked, never independently maintained.
- Non-workflow-backed tasks get the coarser completion semantics the design doc specifies.

## Non-goals
- No input-required state — that's CW-20260814-0016, which depends on this one.
- No JSON-RPC/HTTP transport — that's CW-20260814-0017, which depends on this one.
- No push notification delivery — separate ticket, depends on this one and the transport ticket.

## Boot prompt
You're picking up the core routing ticket for A2A protocol adoption, in the Nanite repo. Before starting, confirm CW-20260814-0013 and CW-20260814-0014 are actually landed. Fetch this task's full description via torque_task_get for complete context, then read the design docs in full. Downstream tickets all depend on this one landing.`

type nullString struct {
	String string `json:"String"`
	Valid  bool   `json:"Valid"`
}

// torqueRecord mirrors Torque's TaskRecord wire shape (declared field order
// matters: DependsOn sits after the long Description).
type torqueRecord struct {
	ID          string     `json:"ID"`
	Title       string     `json:"Title"`
	Description string     `json:"Description"`
	Status      string     `json:"Status"`
	Priority    int        `json:"Priority"`
	Manual      bool       `json:"Manual"`
	Executor    string     `json:"Executor"`
	Tools       nullString `json:"Tools"`
	Permissions nullString `json:"Permissions"`
	Environment nullString `json:"Environment"`
	MaxRetries  int        `json:"MaxRetries"`
	OnDone      string     `json:"OnDone"`
	OnFail      string     `json:"OnFail"`
	OnReview    string     `json:"OnReview"`
	OnDoneMerge string     `json:"OnDoneMerge"`
	DependsOn   nullString `json:"DependsOn"`
	ProjectID   nullString `json:"ProjectID"`
	CreatedAt   string     `json:"CreatedAt"`
	UpdatedAt   string     `json:"UpdatedAt"`
	Kind        string     `json:"Kind"`
	Trust       string     `json:"Trust"`
}

func goldenCases() []goldenCase {
	record, _ := json.MarshalIndent(torqueRecord{
		ID: "CW-20260814-0015", Title: "A2A: Task persistence + TaskManager", Description: torqueDescription,
		Status: "todo", Priority: 3, Manual: true, Executor: "cli", MaxRetries: 3, OnDone: "review", OnFail: "retry",
		OnReview: "pause", OnDoneMerge: "none",
		DependsOn: nullString{String: `["CW-20260814-0013","CW-20260814-0014"]`, Valid: true},
		ProjectID: nullString{String: "PRJ-20260417-0002", Valid: true},
		CreatedAt: "2026-08-14T23:31:34Z", UpdatedAt: "2026-08-14T23:31:34Z", Kind: "agent", Trust: "normal",
	}, "", "  ")

	var lines []string
	for i := range 10 {
		lines = append(lines, strings.Repeat(string(rune('A'+i)), 29))
	}
	lineBody := strings.Join(lines, "\n") + "\n"

	comments := []any{}
	for range 8 {
		comments = append(comments, map[string]any{"content": strings.Repeat("historical investigation ", 200)})
	}
	comments[7] = map[string]any{"content": "DEPLOYMENT VERIFIED; current code fixes discovery"}
	task, _ := json.MarshalIndent(map[string]any{"ok": true, "data": map[string]any{
		"id": "task-example", "title": "Repair discovery", "status": "review", "comments": comments,
		"description": strings.Repeat("Background detail. ", 700),
	}}, "", "  ")

	text := "FIRST SOURCE\n" + strings.Repeat("é", 40000) + "\nLAST SOURCE"
	python, _ := json.Marshal(map[string]any{"stdout": text, "stderr": "diagnostic", "result": nil, "error": "scan stopped"})

	needle := strings.Repeat("prefix ", 1000) + "NEEDLE one\nother\nNEEDLE two\nNEEDLE three\n"
	needleBody, _ := json.Marshal(map[string]any{"stdout": needle})

	unicode := strings.Repeat("é🙂 source line\n", 700)
	unicodeBody, _ := json.Marshal(map[string]any{"stdout": unicode, "a/b~c": []any{json.Number("9007199254740993")}})

	rows := make([]any, 300)
	for i := range rows {
		rows[i] = map[string]any{"id": i, "name": "row", "payload": strings.Repeat("p", 40)}
	}
	array, _ := json.Marshal(rows)

	return []goldenCase{
		{name: "torque_task", body: string(record), budget: 2048, hardCap: 1 << 20,
			fetch:  []map[string]any{{"id": "$ID"}, {"id": "$ID", "json_pointer": "/DependsOn/String"}, {"id": "$ID", "offset": 100, "length": 300}},
			search: []map[string]any{{"id": "$ID", "pattern": "DependsOn"}, {"id": "$ID", "pattern": "absent-token"}}},
		{name: "line_boundary", body: lineBody, budget: 100, hardCap: 1 << 20,
			fetch:  []map[string]any{{"id": "$ID", "length": 45}},
			search: []map[string]any{{"id": "$ID", "pattern": "C+"}}},
		{name: "task_comments", body: string(task), budget: 4000, hardCap: 1 << 20,
			fetch:  []map[string]any{{"id": "$ID", "json_pointer": "/data/comments/7/content"}, {"id": "$ID", "json_pointer": "/data/comments/9"}},
			search: []map[string]any{{"id": "$ID", "pattern": "VERIFIED", "json_pointer": "/data/comments/7/content"}}},
		{name: "python_stdout", body: string(python), budget: 4000, hardCap: 1 << 20,
			fetch:  []map[string]any{{"id": "$ID", "json_pointer": "/stdout", "offset": 39990, "length": 40}},
			search: []map[string]any{{"id": "$ID", "pattern": "SOURCE", "json_pointer": "/stdout"}}},
		{name: "plain_text", body: text, budget: 4000, hardCap: 1 << 20},
		{name: "needle_search", body: string(needleBody), budget: 4000, hardCap: 1 << 20,
			search: []map[string]any{{"id": "$ID", "pattern": "NEEDLE", "json_pointer": "/stdout", "max_matches": 1}, {"id": "$ID", "pattern": "NEEDLE", "json_pointer": "/stdout", "offset": 7017}}},
		{name: "unicode_pages", body: string(unicodeBody), budget: 4000, hardCap: 1 << 20,
			fetch: []map[string]any{{"id": "$ID", "json_pointer": "/stdout", "offset": 1, "length": 6}, {"id": "$ID", "json_pointer": "/a~1b~0c/0"}, {"id": "$ID", "json_pointer": "/a~1b~0c/01"}, {"id": "$ID", "json_pointer": "stdout"}, {"id": "$ID", "offset": -1}}},
		{name: "json_array", body: string(array), budget: 3000, hardCap: 1 << 20,
			fetch: []map[string]any{{"id": "$ID", "json_pointer": "/299/id"}}},
		{name: "over_hard_cap", body: strings.Repeat("x", 1500), budget: 100, hardCap: 1000,
			fetch: []map[string]any{{"id": "$ID"}}, search: []map[string]any{{"id": "$ID", "pattern": "x"}}},
		{name: "bad_args", body: strings.Repeat("x", 500), budget: 100, hardCap: 1000,
			fetch:  []map[string]any{{}, {"id": "$ID", "offset": "1"}, {"id": "$ID", "length": 1.5}, {"id": "nope"}},
			search: []map[string]any{{"id": "$ID"}, {"id": "$ID", "pattern": "("}, {"id": "$ID", "pattern": "x", "max_matches": -1}}},
	}
}
