package main

import (
	"context"
	"fmt"
	"log"

	reflexes "github.com/hollis-labs/go-reflexes"
)

// memory is a fake Source and KindCatalog: a real host reads these from its
// own store.
type memory struct{ rows []reflexes.Reflex }

func (m memory) Candidates(context.Context, string, string) ([]reflexes.Reflex, error) {
	return m.rows, nil
}

func (memory) ActionKinds(context.Context) ([]reflexes.ActionKind, error) {
	return []reflexes.ActionKind{{Name: "inject_reminder", Category: "system_message", CombiningAlgorithm: "all_applicable"}}, nil
}

func main() {
	src := memory{rows: []reflexes.Reflex{{
		ID:          "r1",
		Name:        "wake-on-mail",
		TriggerKind: "event",
		TriggerSpec: `{"name":"mail_received"}`,
		ActionKind:  "inject_reminder",
		ActionSpec:  `{"body":"You have unread mail."}`,
	}}}
	engine, err := reflexes.New(src, src)
	if err != nil {
		log.Fatal(err)
	}
	res, err := engine.Run(context.Background(), reflexes.RunInput{
		AgentID: "a1",
		State:   &reflexes.State{SessionID: "s1", MailUnreadCount: 2},
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, a := range res.Applied.Actions {
		fmt.Println(a.ReflexName, a.ActionKind, a.Spec["body"])
	}
}
