package agentsessions

import (
	"context"
	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/internal/workspacetest"
	"path/filepath"
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
func fixtureStartOptions(t *testing.T, opts StartOptions) StartOptions {
	t.Helper()
	if opts.ArtifactRoot == "" {
		parent := opts.BootDirRoot
		if parent == "" {
			parent = t.TempDir()
		}
		opts.ArtifactRoot = filepath.Join(parent, "candidate")
	}
	opts.ArtifactAuthorization = fixtureAuthorization(t)
	return opts
}
func testPreparePlant(t *testing.T, opts StartOptions, a provider.CLIAdapter, id string) (string, StartOptions, provider.CLIAdapter, error) {
	return preparePlant(context.Background(), fixtureStartOptions(t, opts), a, id)
}
