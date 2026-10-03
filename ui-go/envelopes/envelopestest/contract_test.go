package envelopestest_test

import (
	"context"
	"testing"

	envelopes "github.com/hollis-labs/libs/ui-go/envelopes"
	"github.com/hollis-labs/libs/ui-go/envelopes/envelopestest"
)

// TestContract_selfCheck runs the contract against the package's own
// LoadCore output so the helper is exercised in CI.
func TestContract_selfCheck(t *testing.T) {
	reg, err := envelopes.LoadCore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	envelopestest.RunContract(t, reg)
}
