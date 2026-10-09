package storetest_test

import (
	"testing"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
	"github.com/hollis-labs/go-toolresult/storetest"
)

// ExampleRun shows the whole integration: hand Run a constructor that returns
// a fresh, empty store for every call. It has no Output line because it needs
// a *testing.T; in a real package it is the body of a Test function.
func ExampleRun() {
	var t *testing.T // supplied by the test runner
	storetest.Run(t, func(t *testing.T) toolresult.Store { return memstore.New() })
}
