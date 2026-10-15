package agentlaunch

import (
	"context"
	"github.com/hollis-labs/substrate/harness/internal/workspacetest"
	"testing"
)

func fixtureAuthorization(t *testing.T) ArtifactAuthorizer {
	t.Helper()
	resolve := workspacetest.New(t)
	return func(ctx context.Context, path string) (ArtifactAuthority, error) {
		input, ports, closePorts, err := resolve(ctx, path)
		return ArtifactAuthority{Inactive: true, PrivateCustody: true, Input: input, Ports: ports, Close: closePorts}, err
	}
}
func testMaterializer(t *testing.T, o MaterializerOptions) *DefaultMaterializer {
	o.Authorize = fixtureAuthorization(t)
	return NewDefaultMaterializer(o)
}
func resolveWithAuthority(t *testing.T, ctx context.Context, r PrepareRequest, opts ...SharedPrepareOption) (*PreparedExecution, error) {
	return ResolvePreparation(ctx, r, append(opts, WithArtifactAuthorization(fixtureAuthorization(t)))...)
}

func fixturePrivateDir(t *testing.T) string { t.Helper(); return workspacetest.PrivateDir(t) }
