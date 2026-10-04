package providerplant

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/internal/workspacetest"
	"github.com/hollis-labs/substrate/harness/workspace"
	"os"
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
func plantWithAuthority(t *testing.T, ctx context.Context, p *agentlaunch.PreparedLaunch, opts ...Option) error {
	return Plant(ctx, p, append(opts, WithArtifactAuthorization(fixtureAuthorization(t)))...)
}
func executionWithAuthority(t *testing.T, ctx context.Context, p *agentlaunch.PreparedLaunch, opts ...Option) (*agentlaunch.PreparedExecution, error) {
	return PrepareExecution(ctx, p, append(opts, WithArtifactAuthorization(fixtureAuthorization(t)))...)
}
func prepareWithAuthority(t *testing.T, ctx context.Context, c *agentlaunch.CompiledLaunch, opts ...PrepareAndPlantOption) (*agentlaunch.PreparedLaunch, error) {
	return PrepareAndPlant(ctx, c, append(opts, WithPlantOption(WithArtifactAuthorization(fixtureAuthorization(t))))...)
}
func resolveWithAuthority(t *testing.T, ctx context.Context, r agentlaunch.PrepareRequest, opts ...agentlaunch.SharedPrepareOption) (*agentlaunch.PreparedExecution, error) {
	return agentlaunch.ResolvePreparation(ctx, r, append(opts, agentlaunch.WithArtifactAuthorization(fixtureAuthorization(t)))...)
}

func fixturePrivateDir(t *testing.T) string { t.Helper(); return workspacetest.PrivateDir(t) }

// Projection coverage remains independent of whether an artifact-only caller
// has the typed host inputs required to route a credential reservation.
func projectionForTest(t *testing.T, ctx context.Context, p *agentlaunch.PreparedLaunch, opts ...Option) (*agentlaunch.PreparedExecution, error) {
	t.Helper()
	cfg := plantConfig{resolver: DefaultResolver}
	for _, opt := range opts {
		opt(&cfg)
	}
	adapter, err := resolveAdapter(p.Compiled, cfg)
	if err != nil {
		return nil, err
	}
	return projectPreparedExecution(p, adapter)
}

func requireCredentialRefusal(t *testing.T, p *agentlaunch.PreparedLaunch, err error) {
	t.Helper()
	var r *workspace.Refusal
	if !errors.As(err, &r) || r.Code != workspace.CodeReservedArtifactPath {
		t.Fatalf("credential route not refused: %v", err)
	}
	entries, e := os.ReadDir(p.PlantedBootDir)
	if e != nil || len(entries) != 0 {
		t.Fatalf("refusal mutated boot root: %v %v", entries, e)
	}
}
func projectedFile(t *testing.T, e *agentlaunch.PreparedExecution, path string) string {
	t.Helper()
	for _, f := range e.Artifacts.Entries {
		if f.Path == path {
			return string(f.Bytes)
		}
	}
	t.Fatalf("missing projected artifact %s", path)
	return ""
}
