package ledger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/core/util/ledger"
)

func ExampleMemStore() {
	ctx := context.Background()
	store := &ledger.MemStore{}

	ret := ledger.DefaultRetention() // raw input kept 30 days, everything else forever
	input, _ := ret.Shape(ledger.StageClassifier, json.RawMessage(`{"text":"is this urgent?"}`), "", false, time.Now())
	version := ledger.Version{Label: "set-7", Hash: "9f2c"}

	rec, _, _ := store.Append(ctx, ledger.Record{
		IdempotencyKey: ledger.Key("msg-1", ledger.StageClassifier, version),
		Stage:          ledger.StageClassifier,
		SubjectRef:     "msg-1",
		Input:          input,
		Version:        version,
		Output:         ledger.Output{Label: "question"},
		Disposition:    ledger.Suggested,
	})

	// The same decision again appends nothing.
	_, duplicate, _ := store.Append(ctx, rec)
	fmt.Println("duplicate:", duplicate)

	// A person corrects it. The original row is untouched; the correction is the label.
	store.AppendEvent(ctx, ledger.Event{DecisionID: rec.DecisionID, Kind: ledger.EventCorrection,
		Correction: &ledger.Correction{Label: "urgent", Actor: "urn:example:user:1"}})
	entry, _ := store.Get(ctx, rec.DecisionID)
	label, corrected := entry.Label()
	fmt.Println(label, corrected, entry.Record.Output.Label)
	// Output:
	// duplicate: true
	// urgent true question
}

func ExampleRetention_Shape() {
	ret := ledger.Retention{
		Default: ledger.StageRetention{Mode: ledger.ModeSnapshot, RawTTL: time.Hour},
		Stages: map[ledger.Stage]ledger.StageRetention{
			ledger.StageHuman: {Mode: ledger.ModeNone}, // keep only a hash
		},
	}
	now := time.Unix(0, 0)
	snap, _ := ret.Shape(ledger.StageRule, json.RawMessage(`{"a":1}`), "", false, now)
	none, _ := ret.Shape(ledger.StageHuman, json.RawMessage(`{"a":1}`), "", false, now)
	fmt.Println(snap.Mode, string(snap.Snapshot), snap.RawExpiresAt.Sub(now))
	fmt.Println(none.Mode, len(none.Snapshot), none.Hash == snap.Hash)
	// Output:
	// snapshot {"a":1} 1h0m0s
	// none 0 true
}
