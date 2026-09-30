package agentcontracts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/agent-contracts-leaf/capabilities"
)

func validAssignment() Assignment {
	return Assignment{
		Agent: AgentRef{Name: "incident-triage"},
		Run:   RunPolicy{Lifetime: LifetimeOneShot, Resume: ResumeNever},
		Task:  Task{Input: "triage alert 4412"},
	}
}

func TestAssignmentValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Assignment)
		wantErr string // substring; "" means valid
	}{
		{"valid by name", func(*Assignment) {}, ""},
		{"valid by id", func(a *Assignment) { a.Agent = AgentRef{ID: "agt_01"} }, ""},
		{"no agent", func(a *Assignment) { a.Agent = AgentRef{} }, "one of name or id"},
		{"both name and id", func(a *Assignment) { a.Agent.ID = "agt_01" }, "not both"},
		{"empty input", func(a *Assignment) { a.Task.Input = "" }, "task.input"},
		{"bad lifetime", func(a *Assignment) { a.Run.Lifetime = "forever" }, "run.lifetime"},
		{"empty lifetime", func(a *Assignment) { a.Run.Lifetime = "" }, "run.lifetime"},
		{"bad resume", func(a *Assignment) { a.Run.Resume = "sometimes" }, "run.resume"},
		{"bad isolation", func(a *Assignment) { a.Scope.Isolation = "vm" }, "scope.isolation"},
		{"good isolation", func(a *Assignment) { a.Scope.Isolation = IsolationWorktree }, ""},
		{"bad trust", func(a *Assignment) { a.Trust = "root" }, "trust"},
		{"good trust", func(a *Assignment) { a.Trust = TrustNormal }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := validAssignment()
			tc.mutate(&a)
			err := a.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Validate() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestAssignmentValidateReportsEveryProblem(t *testing.T) {
	err := Assignment{}.Validate()
	if err == nil {
		t.Fatal("empty assignment must not validate")
	}
	for _, want := range []string{"one of name or id", "run.lifetime", "run.resume", "task.input"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestRunPolicy(t *testing.T) {
	ok := RunPolicy{Lifetime: LifetimeLongLived, Attach: true, Attended: false, Resume: ResumeOnFailure}
	if !ok.Valid() || ok.Validate() != nil {
		t.Errorf("valid policy rejected: %v", ok.Validate())
	}
	for _, bad := range []RunPolicy{
		{Lifetime: "x", Resume: ResumeNever},
		{Lifetime: LifetimeOneShot, Resume: "x"},
		{},
	} {
		if bad.Valid() || bad.Validate() == nil {
			t.Errorf("%+v must be invalid", bad)
		}
	}
}

// The addendum's field spellings are the contract; a drift here breaks every
// consumer silently, so they are pinned as a golden document.
func TestAssignmentJSONGolden(t *testing.T) {
	dur := int64(60000)
	toolBytes := int64(1048576)
	turns := 12
	a := Assignment{
		Agent:       AgentRef{Name: "incident-triage", Digest: "sha256:aa", Source: "repo"},
		Scope:       Scope{Project: ProjectRef{ID: "p1", Root: "/work/p1"}, Workdirs: []string{"/work/p1"}, Isolation: IsolationSandbox},
		Grants:      Grants{Tools: []string{"bash"}, Permissions: []string{"allow:read_file"}, MCP: MCPGrant{Allow: []string{"github"}, Deny: []string{"shell"}}},
		Limits:      Limits{MaxDurationMs: &dur, ToolOutputBytes: &toolBytes, MaxTurns: &turns},
		Run:         RunPolicy{Lifetime: LifetimeLongLived, Attach: true, Attended: false, Resume: ResumeOnFailure, Requires: capabilities.Set{capabilities.Resume}},
		Task:        Task{Input: "triage", Files: []string{"alert.json"}},
		Launch:      Launch{Profile: "default", Overrides: LaunchOverrides{Provider: "anthropic", Model: "sonnet"}},
		Trust:       TrustNormal,
		RequestedBy: Requester{ID: "u1", Kind: "user", Extra: map[string]string{"k": "v"}},
		Correlation: Correlation{ID: "c1", Parent: "c0", Trace: "t1"},
		Metadata:    map[string]string{"team": "ops"},
	}
	const golden = `{"agent":{"name":"incident-triage","digest":"sha256:aa","source":"repo"},` +
		`"scope":{"project":{"id":"p1","root":"/work/p1"},"workdirs":["/work/p1"],"isolation":"sandbox"},` +
		`"grants":{"tools":["bash"],"permissions":["allow:read_file"],"mcp":{"allow":["github"],"deny":["shell"]}},` +
		`"limits":{"max_duration_ms":60000,"tool_output_bytes":1048576,"max_turns":12},` +
		`"run":{"lifetime":"long-lived","attach":true,"attended":false,"resume":"on-failure","requires":["resume"]},` +
		`"task":{"input":"triage","files":["alert.json"]},` +
		`"launch":{"profile":"default","overrides":{"provider":"anthropic","model":"sonnet"}},` +
		`"trust":"normal",` +
		`"requested_by":{"id":"u1","kind":"user","extra":{"k":"v"}},` +
		`"correlation":{"id":"c1","parent":"c0","trace":"t1"},` +
		`"metadata":{"team":"ops"}}`

	got, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Errorf("wire form drifted:\n got: %s\nwant: %s", got, golden)
	}
	var back Assignment
	if err := json.Unmarshal([]byte(golden), &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, a) {
		t.Errorf("round trip changed the assignment:\n got: %+v\nwant: %+v", back, a)
	}
}

func TestLaunchRecordJSONRoundTrip(t *testing.T) {
	budget := 2.5
	r := LaunchRecord{
		Digests: Digests{Definition: "sha256:d", Assignment: "sha256:a", LaunchProfile: "sha256:l", Kickoff: "sha256:k"},
		EffectiveGrants: EffectiveGrants{
			Tools:       []string{"bash"},
			Diagnostics: []GrantDiagnostic{{Grant: "isolation:sandbox", Enforced: false, Reason: "host has no sandbox"}},
		},
		EffectiveLimits: Limits{CostBudget: &budget},
		EffectiveTrust:  TrustUntrusted,
		Forced:          []capabilities.Name{capabilities.Sandbox},
		Requester:       Requester{ID: "u1"},
		Timestamp:       time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"digests"`, `"launch_profile"`, `"kickoff"`, `"effective_grants"`, `"effective_limits"`,
		`"effective_trust"`, `"forced"`, `"requester"`, `"timestamp"`, `"diagnostics"`, `"enforced":false`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("wire form lacks %s: %s", key, raw)
		}
	}
	var back LaunchRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, r) {
		t.Errorf("round trip changed the record:\n got: %+v\nwant: %+v", back, r)
	}
}

// There is no launch mode and no instruction-append: the fields that were
// ruled out must not come back under another name.
func TestNoRuledOutFields(t *testing.T) {
	banned := map[string]bool{"Mode": true, "InstructionsAppend": true, "SystemPrompt": true, "AgentFile": true, "Cleanup": true, "Retention": true}
	for _, typ := range []reflect.Type{reflect.TypeOf(Assignment{}), reflect.TypeOf(RunPolicy{}), reflect.TypeOf(Task{}), reflect.TypeOf(Launch{})} {
		for i := 0; i < typ.NumField(); i++ {
			if banned[typ.Field(i).Name] {
				t.Errorf("%s has ruled-out field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}
