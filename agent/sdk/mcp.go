package sdk

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/client"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPCaller is satisfied by the shared MCP Client. Hosts own connection lifecycle,
// authentication, endpoint configuration and catalog/version verification.
type MCPCaller interface {
	CallTool(context.Context, string, map[string]any, ...client.CallOption) (*mcpsdk.CallToolResult, client.CallMetadata, error)
}

// MCPTool binds an explicitly named tool/schema to a host-owned client. Calls
// pass the same SDK policy/gate/admission path as local tools. RetryNever prevents
// a transport failure from silently repeating an uncertain external effect.
// The text-only facade refuses non-text content; hosts can supply their own Tool
// to intentionally project richer content. No protocol payload is put in events.
func MCPTool(def llmtypes.ToolDefinition, versions []runtimecontract.Reference, caller MCPCaller) (Tool, error) {
	if caller == nil {
		return Tool{}, fmt.Errorf("%w: MCP caller required", ErrInvalid)
	}
	return Tool{Definition: def, Versions: versions, Execute: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		result, _, err := caller.CallTool(ctx, def.Name, args, client.WithCallRetry(client.RetryNever))
		if err != nil {
			return ToolResult{}, err
		}
		if result == nil {
			return ToolResult{}, fmt.Errorf("%w: empty MCP result", ErrInvalid)
		}
		if len(result.Content) == 0 && result.StructuredContent != nil {
			raw, e := json.Marshal(result.StructuredContent)
			return ToolResult{Content: string(raw), IsError: result.IsError}, e
		}
		var text string
		for _, content := range result.Content {
			block, ok := content.(*mcpsdk.TextContent)
			if !ok {
				return ToolResult{}, fmt.Errorf("%w: non-text MCP result", ErrUnsupported)
			}
			if text != "" {
				text += "\n"
			}
			text += block.Text
		}
		return ToolResult{Content: text, IsError: result.IsError}, nil
	}}, nil
}
