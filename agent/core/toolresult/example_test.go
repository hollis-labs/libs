package toolresult_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
)

func ExampleCache_Present() {
	cache := toolresult.New(memstore.New(), toolresult.Config{
		NewID: func() string { return "01EXAMPLE" },
		Now:   func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	body := `{"id":"job-7","status":"ok","log":"` + strings.Repeat("line\\n", 1000) + `"}`
	view, err := cache.Present(context.Background(), "session-1", toolresult.Meta{Tool: "run"}, body, 300)
	if err != nil {
		panic(err)
	}
	fmt.Println(view.Cached, view.Format, view.CacheID)
	fmt.Println(view.Footer[:60])
	// Output:
	// true json 01EXAMPLE
	// [TRUNCATED — full result cached as tool_result://01EXAMPLE
}

func ExampleCache_Read() {
	ctx := context.Background()
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	ptr, _ := cache.Put(ctx, "session-1", toolresult.Meta{Tool: "run"}, `{"stdout":"héllo, wörld"}`)

	page, err := cache.Read(ctx, "session-1", ptr.ID, "/stdout", 0, 4, 0)
	fmt.Printf("%q bytes %d..%d of %d more=%t %v\n", page.Content, page.Offset, page.End, page.TotalBytes, page.HasMore, err)

	// Another scope cannot tell this id from an absent one.
	_, err = cache.Read(ctx, "session-2", ptr.ID, "", 0, 0, 0)
	fmt.Println(err != nil)
	// Output:
	// "hél" bytes 0..4 of 14 more=true <nil>
	// true
}

func ExampleCache_HandleFetch() {
	ctx := context.Background()
	cache := toolresult.New(memstore.New(), toolresult.Config{NewID: func() string { return "01EXAMPLE" }})
	_, _ = cache.Put(ctx, "session-1", toolresult.Meta{}, "0123456789")

	// input is the model's decoded tool arguments; the scope is not one of them.
	text, isError := cache.HandleFetch(ctx, "session-1", map[string]any{"id": "01EXAMPLE", "offset": 2.0, "length": 4.0}, 100)
	fmt.Println(isError)
	fmt.Println(text)
	// Output:
	// false
	// [Cached result 01EXAMPLE; json_pointer=""; bytes 2..6 of 10 (end-exclusive); has_more=true; next_offset=6]
	//
	// 2345
}

func ExampleCache_HandleSearch() {
	ctx := context.Background()
	cache := toolresult.New(memstore.New(), toolresult.Config{NewID: func() string { return "01EXAMPLE" }})
	_, _ = cache.Put(ctx, "session-1", toolresult.Meta{}, "alpha\nbeta\ngamma\n")

	text, _ := cache.HandleSearch(ctx, "session-1", map[string]any{"id": "01EXAMPLE", "pattern": "^beta"}, 1000)
	fmt.Print(text)
	// Output:
	// [Cached result 01EXAMPLE; json_pointer=""; matching lines=1; total_bytes=17; has_more=false; next_offset=17]
	//
	// --- Line 2; match bytes 6..10; context bytes 0..17; context_truncated=false ---
	// alpha
	// beta
	// gamma
	//
}

func ExampleCache_FetchSpec() {
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	spec := cache.FetchSpec()
	fmt.Println(spec.Name, spec.InputSchema["required"])
	// Output: fetch_tool_result [id]
}

func ExamplePreview() {
	text, format := toolresult.Preview(`{"id":"a1","status":"done","items":[1,2,3,4,5,6,7,8,9,10]}`, 2000)
	fmt.Println(format)
	fmt.Println(text)
	// Output:
	// json
	// "": object (3 fields)
	// "/id": "a1"
	// "/status": "done"
	// "/items": array (10 items; indices refer to original order)
	// [OMITTED items 2..8, end-exclusive (6 items)]
	// "/items/0": 1
	// "/items/1": 2
	// "/items/8": 9
	// "/items/9": 10
	//
}

func ExampleBudgetForWindow() {
	fmt.Println(toolresult.BudgetForWindow(200_000, toolresult.BudgetOptions{}))
	fmt.Println(toolresult.BudgetForWindow(1_000_000, toolresult.BudgetOptions{}))
	// Output:
	// 4000
	// 16000
}

func ExampleSelect() {
	v, _ := toolresult.Select(`{"a/b":{"n":[7,"x"]}}`, "/a~1b/n/1")
	fmt.Println(v)
	// Output: x
}

func ExampleReadPage() {
	p, _ := toolresult.ReadPage("aé🙂z", 1, 3, 10)
	fmt.Printf("%q %d..%d more=%t\n", p.Content, p.Offset, p.End, p.HasMore)
	// Output: "é" 1..3 more=true
}

func ExampleSearchPage() {
	r, _ := toolresult.SearchPage("one\ntwo\nthree\n", "t", 0, 10, 1000)
	for _, m := range r.Matches {
		fmt.Println(m.Line)
	}
	// Output:
	// 2
	// 3
}

func ExampleCutUTF8() {
	head, cut := toolresult.CutUTF8("héllo", 2)
	fmt.Printf("%q %t\n", head, cut)
	// Output: "h" true
}
