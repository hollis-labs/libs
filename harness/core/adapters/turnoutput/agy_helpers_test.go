package turnoutput

import (
	"testing"

	gop "github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/adapters/providertest"
)

func newAgy() *gop.AntigravityAdapter { return gop.NewAntigravityAdapter() }

func agyFixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	return providertest.FixtureLines(t, name)
}
