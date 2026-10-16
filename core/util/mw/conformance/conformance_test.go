package conformance_test

import (
	"testing"

	"github.com/hollis-labs/libs/core/util/mw/conformance"
)

func TestGeneric(t *testing.T) { conformance.Run(t, conformance.Generic{}) }
func TestJob(t *testing.T)     { conformance.Run(t, conformance.Job{}) }
func TestHTTP(t *testing.T)    { conformance.Run(t, conformance.HTTP{}) }
