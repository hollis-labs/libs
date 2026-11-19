package memstore_test

import (
	"testing"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	"github.com/hollis-labs/libs/agent/chat/memstore"
)

func TestControlConformance(t *testing.T) {
	chattest.ControlConformance(t, func(*testing.T) chat.Control { return memstore.NewControl() })
}
