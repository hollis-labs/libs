package plant

import (
	"context"
	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/internal/workspacetest"
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

func fixturePrivateDir(t *testing.T) string { t.Helper(); return workspacetest.PrivateDir(t) }
