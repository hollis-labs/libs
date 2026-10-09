package loopdetect_test

import (
	"encoding/json"
	"fmt"

	loopdetect "github.com/hollis-labs/go-loopdetect"
)

func ExampleDetector_Record() {
	d := loopdetect.New()
	args := json.RawMessage(`{"path":"main.go"}`)
	var det loopdetect.Detection
	var found bool
	for turn := 1; turn <= 3; turn++ {
		det, found = d.Record(loopdetect.Signal{
			SessionID: "session-1",
			TurnID:    fmt.Sprintf("turn-%d", turn),
			ToolName:  "read_file",
			Args:      args,
		})
	}
	fmt.Println(found)
	fmt.Println(det.ToolName, det.Count, det.WindowSize, det.DetectedAtTurn)
	fmt.Println(det.Reason)
	// Output:
	// true
	// read_file 3 3 turn-3
	// fingerprint_repeated_3_times_in_last_3
}
