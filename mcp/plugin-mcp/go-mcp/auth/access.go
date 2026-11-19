package auth

import (
	"context"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// AccessPolicy decides whether an authenticated caller may execute a tool.
// It is evaluated on every call; treat info and def as read-only. Policy owns
// scope/identity semantics. Tool annotations alone are not authorization.
type AccessPolicy func(ctx context.Context, info *sdkauth.TokenInfo, def server.ToolDefinition) bool

// CanAccess evaluates policy with the verified bearer identity from the SDK's
// HTTP auth context. Missing authentication or a nil policy denies access,
// including stdio and direct CallTool calls with an unauthenticated context.
// It never reads identity from arguments, _meta, or forwarded headers.
func CanAccess(ctx context.Context, def server.ToolDefinition, policy AccessPolicy) bool {
	info := sdkauth.TokenInfoFromContext(ctx)
	return info != nil && policy != nil && policy(ctx, info, def)
}

// AuthorizeTools enforces CanAccess before a tool handler executes. Install
// with server.WithToolMiddleware. It applies to wire and direct CallTool calls,
// but does not filter tools/list or tools registered directly on SDKServer().
// A denial is a generic forbidden tool error; no token or policy details are
// returned. This is local authorization, not an OAuth scope step-up flow.
func AuthorizeTools(policy AccessPolicy) server.ToolMiddleware {
	return func(def server.ToolDefinition, next server.ToolHandler) server.ToolHandler {
		return func(ctx context.Context, args map[string]any) (any, error) {
			if !CanAccess(ctx, def, policy) {
				return nil, budget.NewToolError("forbidden", "access denied")
			}
			return next(ctx, args)
		}
	}
}
