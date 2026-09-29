// Command hello previews an oversized tool result, then reads a field of the
// original back through the pointer.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
)

func main() {
	ctx := context.Background()
	cache := toolresult.New(memstore.New(), toolresult.Config{})

	// A tool result far bigger than the 2 KiB default budget.
	body := `{"ok":true,"stdout":"` + strings.Repeat("line of output\\n", 400) + `"}`

	// The scope is the host's authenticated session, never a tool argument.
	view, err := cache.Present(ctx, "session-1", toolresult.Meta{Tool: "shell", CallID: "call-1"}, body, 0)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("cached:", view.Cached, "format:", view.Format, "original bytes:", view.OriginalBytes)

	// The model follows the pointer: the first 32 bytes of /stdout.
	page, err := cache.Read(ctx, "session-1", view.CacheID, "/stdout", 0, 32, 0)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("bytes %d..%d of %d, has_more=%t\n", page.Offset, page.End, page.TotalBytes, page.HasMore)
}
