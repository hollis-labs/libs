package memstore_test

import (
	"context"
	"fmt"
	"time"

	"github.com/hollis-labs/substrate/mesh/hitl"
	"github.com/hollis-labs/substrate/mesh/hitl/memstore"
)

func ExampleNew() {
	s := memstore.New()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	rec := hitl.Record{
		ItemID: "item_1", CallerScope: "app", IdempotencyKey: "k", Digest: "d", Kind: "approval",
		Request: []byte(`{}`), State: hitl.StatePresented, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, created, _ := s.Create(context.Background(), rec)
	_, again, _ := s.Create(context.Background(), rec) // same scope and key: no second record
	fmt.Println(created, again)
	// Output: true false
}
