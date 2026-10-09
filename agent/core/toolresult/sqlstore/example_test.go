package sqlstore_test

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/sqlstore"
)

func ExampleNew() {
	db, _ := sql.Open("sqlite", ":memory:")
	db.SetMaxOpenConns(1)
	defer db.Close()

	// Loom's existing table and scope column; Nanite's is the zero Table.
	table := sqlstore.Table{Name: "wiki_result_cache", ScopeColumn: "caller_id"}
	if _, err := db.Exec(sqlstore.DDL(table)); err != nil {
		panic(err)
	}

	cache := toolresult.New(sqlstore.New(db, table), toolresult.Config{})
	ctx := context.Background()
	ptr, _ := cache.Put(ctx, "caller-1", toolresult.Meta{Tool: "wiki_search"}, "persisted")
	page, _ := cache.Read(ctx, "caller-1", ptr.ID, "", 0, 0, 0)
	fmt.Println(page.Content)
	// Output: persisted
}

func ExampleDDL() {
	fmt.Println(sqlstore.DDL(sqlstore.Table{})[:44])
	// Output: CREATE TABLE IF NOT EXISTS tool_result_cache
}
