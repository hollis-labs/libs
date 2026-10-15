package contexthook

import (
	"context"
	"github.com/hollis-labs/libs/agent/core/agentcontext"
	"github.com/hollis-labs/libs/harness/core/agentlaunch"
	"github.com/hollis-labs/libs/harness/core/internal/workspacetest"
	"testing"
)

func fixtureAuthorization(t *testing.T) agentlaunch.ArtifactAuthorizer {
	t.Helper()
	resolve := workspacetest.New(t)
	return func(ctx context.Context, path string) (agentlaunch.ArtifactAuthority, error) {
		input, ports, closePorts, err := resolve(ctx, path)
		return agentlaunch.ArtifactAuthority{Inactive: true, PrivateCustody: true, Input: input, Ports: ports, Close: closePorts}, err
	}
}
func hookWithAuthority(t *testing.T, p agentcontext.ContextProvider, c Config) agentlaunch.ContextHook {
	c.Authorize = fixtureAuthorization(t)
	return New(p, c)
}
