package storetest_test

import (
	"testing"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention/storetest"
)

func TestMemHeadOnly(t *testing.T) {
	storetest.Run(t, func(*testing.T) storetest.Fixture { return storetest.NewMem(retention.TrimHeadOnly) })
}

func TestMemPerRow(t *testing.T) {
	storetest.Run(t, func(*testing.T) storetest.Fixture { return storetest.NewMem(retention.TrimPerRow) })
}
