package memstore_test

import (
	"testing"

	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/hitl/hitltest"
	"github.com/hollis-labs/libs/mesh/core/hitl/memstore"
)

func TestStoreContract(t *testing.T) {
	hitltest.RunStoreContract(t, func(*testing.T) hitl.Store { return memstore.New() })
}
