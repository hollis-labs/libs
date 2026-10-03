package memstore_test

import (
	"testing"

	"github.com/hollis-labs/substrate/mesh/hitl"
	"github.com/hollis-labs/substrate/mesh/hitl/hitltest"
	"github.com/hollis-labs/substrate/mesh/hitl/memstore"
)

func TestStoreContract(t *testing.T) {
	hitltest.RunStoreContract(t, func(*testing.T) hitl.Store { return memstore.New() })
}
