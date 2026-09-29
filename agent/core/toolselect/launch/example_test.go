package launch_test

import (
	"fmt"

	contracts "github.com/hollis-labs/agent-contracts-leaf"

	"github.com/hollis-labs/go-toolselect/launch"
	"github.com/hollis-labs/go-toolselect/profile"
)

func ExampleFromAssignment() {
	base := profile.Profile{ID: "coder", ToolsAllow: []string{"github_*", "files_read"}}
	a := contracts.Assignment{Grants: contracts.Grants{MCP: contracts.MCPGrant{
		Allow: []string{"github_*"},
		Deny:  []string{"github_pr_merge"},
	}}}
	derived, err := launch.FromAssignment(base, a)
	if err != nil {
		fmt.Println(err)
		return
	}
	catalog := profile.Catalog{Servers: []profile.Server{
		{ID: "github", Tools: []profile.Tool{{Name: "github_issue_get"}, {Name: "github_pr_merge"}}},
		{ID: "files", Tools: []profile.Tool{{Name: "files_read"}}},
	}}
	visible, hidden, err := profile.Evaluate(catalog, derived)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, v := range visible {
		fmt.Println("visible:", v.Name)
	}
	for _, h := range hidden {
		fmt.Println("hidden:", h.Name, h.Reason)
	}
	// Output:
	// visible: github_issue_get
	// hidden: github_pr_merge denied
	// hidden: files_read not_allowed
}
