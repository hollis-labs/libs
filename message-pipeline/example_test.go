package pipeline_test

import (
	"context"
	"fmt"

	pipeline "github.com/hollis-labs/libs/message-pipeline"
	"time"
)

// The in-memory example store is not durable. Production adapters implement the
// same port with private durable custody and their own sink/cursor transaction.
type exampleStore map[pipeline.Key]pipeline.Record

func (s exampleStore) Get(_ context.Context, k pipeline.Key) (pipeline.Record, bool, error) {
	r, ok := s[k]
	return r.Clone(), ok, nil
}
func (s exampleStore) PutIfAbsent(_ context.Context, k pipeline.Key, r pipeline.Record) (pipeline.Record, error) {
	if old, ok := s[k]; ok {
		return old.Clone(), nil
	}
	s[k] = r.Clone()
	return r.Clone(), nil
}
func ExampleNew() {
	r, err := pipeline.New([]pipeline.StageSpec{{ID: "summary", Version: "1", Timeout: time.Second, ConfigDigest: "snapshot-v1", InstructionDigest: "instruction-v1", Stage: pipeline.StageFunc(func(context.Context, pipeline.Input) (pipeline.Result, error) {
		return pipeline.Result{Summaries: []pipeline.Summary{{Text: "A short summary."}}}, nil
	})}}, exampleStore{}, pipeline.Options{MaxOriginalBytes: 1024})
	if err != nil {
		panic(err)
	}
	result, err := r.Run(context.Background(), pipeline.Message{Identity: pipeline.Identity{Source: "endpoint/channel", Message: "publication-1"}, Original: "The original remains unchanged."}, pipeline.State{})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Disposition, result.Message.Original, result.State.Annotations[0].Summary.Text)
	// Output: pass The original remains unchanged. A short summary.
}
