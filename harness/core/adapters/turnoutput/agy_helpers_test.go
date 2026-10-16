package turnoutput

import (
	"testing"

	gop "github.com/hollis-labs/libs/harness/core/adapters/provider"
	"github.com/hollis-labs/libs/harness/core/adapters/providertest"
)

func newAgy() *gop.AntigravityAdapter { return gop.NewAntigravityAdapter() }

func agyFixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	return providertest.FixtureLines(t, name)
}
