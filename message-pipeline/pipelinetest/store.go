// Package pipelinetest provides result-store conformance cases for adapters.
package pipelinetest

import (
	"context"
	"errors"
	"testing"

	pipeline "github.com/hollis-labs/libs/message-pipeline"
)

// CheckResultStore checks an empty, privately owned adapter store. It does not
// create a database or verify crash durability; the adapter supplies that proof.
func CheckResultStore(t *testing.T, store pipeline.ResultStore) {
	t.Helper()
	ctx := context.Background()
	key := pipeline.Key{Message: pipeline.Identity{Source: "conformance/channel", Message: t.Name()}, StageID: "summary", StageVersion: "1", ConfigDigest: "configuration", InstructionDigest: "instruction", TimeoutNS: 1, FailMode: pipeline.FailOpen}
	if _, found, err := store.Get(ctx, key); err != nil || found {
		t.Fatalf("empty lookup: found=%v err=%v", found, err)
	}
	record := pipeline.Record{SchemaVersion: 1, InputDigest: "immutable-input", Result: pipeline.Result{Disposition: pipeline.Pass, Summaries: []pipeline.Summary{{Text: "original summary"}}}, Trace: pipeline.Trace{StageID: "summary", StageVersion: "1", Outcome: pipeline.Passed}}
	saved, err := store.PutIfAbsent(ctx, key, record.Clone())
	if err != nil {
		t.Fatal(err)
	}
	if saved.InputDigest != record.InputDigest || saved.Result.Summaries[0].Text != "original summary" {
		t.Fatal("wrong initial receipt")
	}
	record.Result.Summaries[0].Text = "mutated caller buffer"
	got, found, err := store.Get(ctx, key)
	if err != nil || !found || got.Result.Summaries[0].Text != "original summary" {
		t.Fatal("store retained mutable caller state")
	}
	competing := got.Clone()
	competing.InputDigest = "different-input"
	competing.Result.Summaries[0].Text = "conflicting summary"
	winner, err := store.PutIfAbsent(ctx, key, competing)
	if err != nil && !errors.Is(err, pipeline.ErrConflict) {
		t.Fatal(err)
	}
	if err == nil && (winner.InputDigest != got.InputDigest || winner.Result.Summaries[0].Text != "original summary") {
		t.Fatal("contender overwrote winner")
	}
	replay, found, err := store.Get(ctx, key)
	if err != nil || !found || replay.InputDigest != got.InputDigest || replay.Result.Summaries[0].Text != "original summary" {
		t.Fatal("saved winner changed")
	}
	failedKey := key
	failedKey.StageID = "failed"
	failure := pipeline.Record{SchemaVersion: 1, InputDigest: "immutable-input", Result: pipeline.Result{Disposition: pipeline.Pass}, Trace: pipeline.Trace{StageID: "failed", StageVersion: "1", Outcome: pipeline.Failed, FailureCode: pipeline.StageError}}
	if _, err = store.PutIfAbsent(ctx, failedKey, failure); err != nil {
		t.Fatal(err)
	}
	persisted, found, err := store.Get(ctx, failedKey)
	if err != nil || !found || persisted.Trace.FailureCode != pipeline.StageError || persisted.Trace.Outcome != pipeline.Failed {
		t.Fatal("failure outcome lost")
	}
}
