package memstore_test

import (
	"testing"

	"github.com/hollis-labs/go-hitl"
	"github.com/hollis-labs/go-hitl/hitltest"
	"github.com/hollis-labs/go-hitl/memstore"
)

func TestStoreContract(t *testing.T) {
	hitltest.RunStoreContract(t, func(*testing.T) hitl.Store { return memstore.New() })
}
