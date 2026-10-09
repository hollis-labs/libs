package pipeline_test

import (
	pipeline "github.com/hollis-labs/libs/message-pipeline"
	"github.com/hollis-labs/libs/message-pipeline/pipelinetest"
	"testing"
)

func TestExampleStoreConformance(t *testing.T) { pipelinetest.CheckResultStore(t, exampleStore{}) }

var _ pipeline.ResultStore = exampleStore{}
