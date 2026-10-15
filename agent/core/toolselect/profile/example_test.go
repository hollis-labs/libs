package profile_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/agent/toolselect/profile"
)

func ExampleEvaluate() {
	yes := true
	catalog := profile.Catalog{Servers: []profile.Server{
		{ID: "torque", Tools: []profile.Tool{
			{Name: "torque_task_list", ReadOnly: &yes},
			{Name: "torque_task_create"},
		}},
		{ID: "files", Tools: []profile.Tool{{Name: "files_read", ReadOnly: &yes}}},
	}}
	visible, hidden, err := profile.Evaluate(catalog, profile.Profile{
		Servers:  map[string]bool{"files": false},
		ReadOnly: true,
	})
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
	// visible: torque_task_list
	// hidden: torque_task_create read_only
	// hidden: files_read server_disabled
}
