// Command hello ranks a small tool catalog and evaluates a visibility profile.
package main

import (
	"fmt"
	"log"

	toolselect "github.com/hollis-labs/go-toolselect"
	"github.com/hollis-labs/go-toolselect/profile"
)

func main() {
	catalog := toolselect.Catalog{Tools: []toolselect.Tool{
		{Server: "torque", Name: "torque_task_create", Description: "Create a task in the tracker"},
		{Server: "torque", Name: "torque_task_list", Description: "List tasks"},
		{Server: "files", Name: "files_read", Description: "Read a file from disk"},
	}}
	hits, err := toolselect.Rank(catalog, "create task", nil, toolselect.WithMaxResults(2))
	if err != nil {
		log.Fatal(err)
	}
	for _, h := range hits {
		fmt.Println(h.Tier, h.Tool.Name)
	}

	yes := true
	visible, hidden, err := profile.Evaluate(
		profile.Catalog{Servers: []profile.Server{
			{ID: "torque", Tools: []profile.Tool{
				{Name: "torque_task_list", ReadOnly: &yes},
				{Name: "torque_task_create"}, // hint undeclared: nil stays nil
			}},
			{ID: "files", Tools: []profile.Tool{{Name: "files_read", ReadOnly: &yes}}},
		}},
		profile.Profile{Servers: map[string]bool{"files": false}, ReadOnly: true},
	)
	if err != nil {
		log.Fatal(err)
	}
	for _, v := range visible {
		fmt.Println("visible:", v.Name)
	}
	for _, h := range hidden {
		fmt.Println("hidden:", h.Name, h.Reason)
	}
}
