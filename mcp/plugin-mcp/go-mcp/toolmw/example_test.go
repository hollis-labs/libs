package toolmw_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/toolmw"
)

func ExampleStack_Middleware() {
	stack, err := toolmw.Assemble(mw.Options{},
		toolmw.Recover(),
		toolmw.Wrap(mw.Descriptor{Name: "strict-args", Phase: mw.PhaseLimits, Failure: mw.Closed}, server.StrictArgs()),
		toolmw.DenyDestructive(),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	srv := server.NewServer("example", "0.0.0", server.WithToolMiddleware(stack.Middleware()))

	schema := server.ObjectSchema(map[string]any{"id": map[string]any{"type": "string"}})
	handler := func(context.Context, map[string]any) (any, error) { return "done", nil }
	srv.RegisterChecked(server.Tool{Name: "read_item", Description: "Read an item.", InputSchema: schema, Handler: handler}, server.Reads("lookup only"))
	srv.RegisterChecked(server.Tool{Name: "drop_item", Description: "Delete an item.", InputSchema: schema, Handler: handler}, server.Destroys("removes data"))

	ctx := context.Background()
	out, err := srv.CallTool(ctx, "read_item", map[string]any{"id": "a"})
	fmt.Println(out, err)

	_, err = srv.CallTool(ctx, "drop_item", map[string]any{"id": "a"})
	var te *budget.ToolError
	fmt.Println(errors.As(err, &te), te.Code)
	// Output:
	// done <nil>
	// true permission_denied
}
