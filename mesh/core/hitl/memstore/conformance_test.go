package memstore_test

import (
	"testing"

	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/hitl/hitltest"
	"github.com/hollis-labs/libs/mesh/core/hitl/memstore"
)

// TestReferenceServiceConformance runs the reference Service over memstore
// through every conformance scenario, with all capabilities declared.
func TestReferenceServiceConformance(t *testing.T) {
	svc := hitl.NewService(memstore.New(), hitl.Options{})
	hitltest.RunConformance(t, hitltest.NewServiceAdapter(svc), hitltest.ServiceCaps()...)
}
