package storetest_test

import (
	"testing"

	toolresult "github.com/hollis-labs/libs/agent/core/toolresult"
	"github.com/hollis-labs/libs/agent/core/toolresult/memstore"
	"github.com/hollis-labs/libs/agent/core/toolresult/storetest"
)

// TestSuiteRunsAgainstMemstore keeps the suite itself compiled and exercised
// from its own package; the store packages run it for real.
func TestSuiteRunsAgainstMemstore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) toolresult.Store { return memstore.New() })
}
