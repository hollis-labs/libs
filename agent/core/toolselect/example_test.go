package toolselect_test

import (
	"fmt"

	toolselect "github.com/hollis-labs/go-toolselect"
)

func ExampleRank() {
	catalog := toolselect.Catalog{Tools: []toolselect.Tool{
		{Server: "torque", Name: "torque_task_create", Description: "Create a task in the tracker"},
		{Server: "torque", Name: "torque_task_list", Description: "List tasks"},
		{Server: "files", Name: "files_read", Description: "Read a file from disk"},
	}}
	hits, err := toolselect.Rank(catalog, "create task", nil, toolselect.WithMaxResults(2))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, h := range hits {
		fmt.Println(h.Tier, h.Tool.Name)
	}
	// Output:
	// bm25 torque_task_create
	// bm25 torque_task_list
}

func ExampleIndex_Rank_pin() {
	idx, err := toolselect.NewIndex(toolselect.Catalog{Tools: []toolselect.Tool{
		{Name: "alpha_search", Description: "search things"},
		{Name: "beta_search", Description: "search other things"},
	}})
	if err != nil {
		fmt.Println(err)
		return
	}
	rules := []toolselect.Rule{{
		Action: toolselect.Action{Type: toolselect.ActionOrder, Pin: []string{"beta_search"}},
	}}
	hits, _ := idx.Rank("search", rules)
	for _, h := range hits {
		fmt.Println(h.Tier, h.Tool.Name)
	}
	// Output:
	// pinned beta_search
	// bm25 alpha_search
}
