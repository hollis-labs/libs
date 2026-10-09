package memstore_test

import (
	"context"
	"fmt"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
)

func ExampleNew() {
	ctx := context.Background()
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	ptr, _ := cache.Put(ctx, "session-1", toolresult.Meta{Tool: "run"}, "stored in memory")
	page, _ := cache.Read(ctx, "session-1", ptr.ID, "", 0, 0, 0)
	fmt.Println(page.Content)
	// Output: stored in memory
}
