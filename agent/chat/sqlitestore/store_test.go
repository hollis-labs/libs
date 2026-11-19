package sqlitestore_test

import (
	"context"
	"path/filepath"
	"testing"

	chat "github.com/hollis-labs/libs/agent/chat"
	"github.com/hollis-labs/libs/agent/chat/chattest"
	"github.com/hollis-labs/libs/agent/chat/sqlitestore"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui/ui-go/streamhub/hubtest"
)

func open(t *testing.T, path string, opts ...sqlitestore.Option) *sqlitestore.Store {
	t.Helper()
	st, err := sqlitestore.Open(context.Background(), path, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st
}

func factory(page int) hubtest.Factory {
	paths := map[streamhub.Log]string{}
	return hubtest.Factory{
		Idempotent: true,
		New: func(t *testing.T) streamhub.Log {
			path := filepath.Join(t.TempDir(), "chat.db")
			st := open(t, path, sqlitestore.WithPageSize(page))
			paths[st] = path
			return st
		},
		Reopen: func(t *testing.T, l streamhub.Log) streamhub.Log {
			if err := l.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			return open(t, paths[l], sqlitestore.WithPageSize(page))
		},
	}
}

func TestLogConformance(t *testing.T) {
	t.Run("page7", func(t *testing.T) { hubtest.Conformance(t, factory(7)) })
	t.Run("defaultPage", func(t *testing.T) { hubtest.Conformance(t, factory(256)) })
}

func TestLogHubSuite(t *testing.T) {
	hubtest.HubSuite(t, factory(256))
}

func TestControlConformance(t *testing.T) {
	chattest.ControlConformance(t, func(t *testing.T) chat.Control {
		st := open(t, filepath.Join(t.TempDir(), "chat.db"))
		t.Cleanup(func() { _ = st.Close() })
		return st
	})
}
