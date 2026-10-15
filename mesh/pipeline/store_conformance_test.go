package pipeline_test

import (
	pipeline "github.com/hollis-labs/libs/mesh/pipeline"
	"github.com/hollis-labs/libs/mesh/pipeline/pipelinetest"
	"testing"
)

func TestExampleStoreConformance(t *testing.T) { pipelinetest.CheckResultStore(t, exampleStore{}) }

var _ pipeline.ResultStore = exampleStore{}
