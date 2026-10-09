package storetest_test

import (
	"testing"

	toolresult "github.com/hollis-labs/substrate/agent/toolresult"
	"github.com/hollis-labs/substrate/agent/toolresult/memstore"
	"github.com/hollis-labs/substrate/agent/toolresult/storetest"
)

// TestSuiteRunsAgainstMemstore keeps the suite itself compiled and exercised
// from its own package; the store packages run it for real.
func TestSuiteRunsAgainstMemstore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) toolresult.Store { return memstore.New() })
}
