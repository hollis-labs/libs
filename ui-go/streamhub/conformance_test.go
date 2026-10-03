package streamhub_test

import (
	"testing"

	streamhub "github.com/hollis-labs/go-streamhub"
	"github.com/hollis-labs/go-streamhub/hubtest"
)

func memoryFactory(pageSize int) hubtest.Factory {
	return hubtest.Factory{
		New: func(*testing.T) streamhub.Log {
			return streamhub.NewMemoryLog(streamhub.WithPageSize(pageSize))
		},
		// A MemoryLog has nothing to reopen from: the "reopened" log is the
		// same log, which checks Head/After/Append after a Trim.
		Reopen: func(_ *testing.T, l streamhub.Log) streamhub.Log { return l },
	}
}

func TestMemoryLogConformance(t *testing.T) {
	t.Run("page7", func(t *testing.T) { hubtest.Conformance(t, memoryFactory(7)) })
	t.Run("defaultPage", func(t *testing.T) { hubtest.Conformance(t, memoryFactory(128)) })
}

func TestMemoryLogHubSuite(t *testing.T) {
	hubtest.HubSuite(t, memoryFactory(16))
}
