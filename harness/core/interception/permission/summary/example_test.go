package summary_test

import (
	"fmt"

	permission "github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/interception/permission/summary"
)

func ExampleRenderPermissionSummary() {
	out := summary.RenderPermissionSummary(summary.SummaryInput{
		Rules: &permission.RuleSet{Rules: []permission.Rule{
			{Tool: "read_file", Pattern: "/srv/secrets/**", Behavior: permission.DecisionDeny, Source: "profile.yaml"},
		}},
		OwnGrants: []string{"/srv/app/config.yaml"},
	})
	fmt.Println(out)
	// Output:
	// ## Path access
	//
	// You have explicit access to (session grants):
	//   - /srv/app/config.yaml
	//
	// You CANNOT access (explicitly denied):
	//   - `read_file` on `/srv/secrets/**`  (from profile.yaml)
	//
	// Any path not listed above is outside this session's scope. If a task needs an out-of-scope path, return a failure naming the path rather than synthesizing an answer.
}
