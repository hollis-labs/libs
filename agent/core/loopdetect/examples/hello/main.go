package main

import (
	"encoding/json"
	"fmt"

	loopdetect "github.com/hollis-labs/go-loopdetect"
)

func main() {
	d := loopdetect.New()
	args := json.RawMessage(`{"path":"main.go"}`)
	for turn := 1; turn <= 3; turn++ {
		det, found := d.Record(loopdetect.Signal{
			SessionID: "session-1",
			TurnID:    fmt.Sprintf("turn-%d", turn),
			ToolName:  "read_file",
			Args:      args,
		})
		fmt.Println(turn, found, det.Reason)
	}
}
