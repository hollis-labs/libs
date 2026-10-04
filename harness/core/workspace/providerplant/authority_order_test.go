package providerplant

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func TestActivePlantAuthorityBeforeResolver(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			isolateHome(t)
			prepared := preparedFor(t, "claude", runtimes.ModePTY)
			resolved := false
			authorizations, closes, records, projected := 0, 0, 0, 0
			adapter := &authorityProjectionAdapter{ClaudeAdapter: provider.NewClaudeAdapter(), calls: &projected}
			opts := []Option{WithResolver(func(*agentlaunch.CompiledLaunch) (provider.BootDirProvider, error) {
				resolved = true
				return adapter, nil
			})}
			if kind == "invalid" {
				base := fixtureAuthorization(t)
				opts = append(opts, WithArtifactAuthorization(func(ctx context.Context, path string) (agentlaunch.ArtifactAuthority, error) {
					authorizations++
					a, e := base(ctx, path)
					closePorts := a.Close
					a.Close = func() error { closes++; return closePorts() }
					a.Ports.ReceiptStore = &authorityOrderStore{a.Ports.ReceiptStore, &records}
					a.Inactive = false
					return a, e
				}))
			}
			ctx := context.Background()
			if kind == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
				opts = append(opts, WithArtifactAuthorization(func(context.Context, string) (agentlaunch.ArtifactAuthority, error) {
					authorizations++
					return agentlaunch.ArtifactAuthority{}, nil
				}))
			}
			_, err := PrepareExecution(ctx, prepared, opts...)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if resolved {
				t.Fatalf("resolver invoked before authority refusal: kind=%s err=%v", kind, err)
			}
			if projected != 0 || records != 0 || kind == "invalid" && closes != 1 || kind == "cancelled" && authorizations != 0 {
				t.Fatal("authority lifecycle crossed refusal", projected, records, closes, authorizations)
			}
			if kind == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				var refusal *workspace.Refusal
				if !errors.As(err, &refusal) || refusal.Status != workspace.Unsupported {
					t.Fatal("untyped authority refusal", err)
				}
			}
			entries, e := os.ReadDir(prepared.PlantedBootDir)
			if e != nil || len(entries) != 0 {
				t.Fatal("candidate mutated", entries, e)
			}
		})
	}
}

type authorityOrderStore struct {
	workspace.ReceiptStore
	records *int
}

func (s *authorityOrderStore) Record(ctx context.Context, r workspace.Receipt) error {
	*s.records++
	return s.ReceiptStore.Record(ctx, r)
}

type authorityProjectionAdapter struct {
	*provider.ClaudeAdapter
	calls  *int
	fail   bool
	cancel func()
}

func (a *authorityProjectionAdapter) ProviderProjection(pc provider.PlantContext, o provider.ProjectionOptions) (provider.ProviderProjection, error) {
	*a.calls++
	if a.cancel != nil {
		a.cancel()
	}
	if a.fail {
		return provider.ProviderProjection{}, errors.New("fixture projection failure")
	}
	return a.ClaudeAdapter.ProviderProjection(pc, o)
}

func TestPlantClosesOneResolvedAuthorityAndPreservesPureProjection(t *testing.T) {
	for _, outcome := range []string{"success", "projection error", "cancel during resolver", "cancel during projection", "pure"} {
		t.Run(outcome, func(t *testing.T) {
			isolateHome(t)
			prepared := preparedFor(t, "claude", runtimes.ModePTY)
			resolve := fixtureAuthorization(t)
			resolves, closes, records, projected := 0, 0, 0, 0
			authorize := func(ctx context.Context, path string) (agentlaunch.ArtifactAuthority, error) {
				resolves++
				a, err := resolve(ctx, path)
				closePorts := a.Close
				a.Close = func() error { closes++; return closePorts() }
				a.Ports.ReceiptStore = &authorityOrderStore{a.Ports.ReceiptStore, &records}
				return a, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			adapter := &authorityProjectionAdapter{ClaudeAdapter: provider.NewClaudeAdapter(), calls: &projected, fail: outcome == "projection error"}
			if outcome == "cancel during projection" {
				adapter.cancel = cancel
			}
			opts := []Option{WithArtifactAuthorization(authorize), WithResolver(func(*agentlaunch.CompiledLaunch) (provider.BootDirProvider, error) {
				if outcome == "cancel during resolver" {
					cancel()
				}
				return adapter, nil
			})}
			if outcome == "pure" {
				execution, err := ProjectExecution(ctx, prepared, opts...)
				if err != nil || execution.Materialization != nil || projected != 1 || resolves != 0 || closes != 0 || records != 0 {
					t.Fatal("pure projection changed", err)
				}
				return
			}
			_, err := PrepareExecution(ctx, prepared, opts...)
			if resolves != 1 || closes != 1 {
				t.Fatal("authority ownership changed", resolves, closes, err)
			}
			if outcome == "success" {
				if err != nil || records == 0 || projected != 1 {
					t.Fatal("valid authority refused", err)
				}
			} else {
				if err == nil || records != 0 || outcome == "cancel during resolver" && projected != 0 {
					t.Fatal("failure crossed boundary", err, records, projected)
				}
				entries, e := os.ReadDir(prepared.PlantedBootDir)
				if e != nil || len(entries) != 0 {
					t.Fatal("failure mutated candidate", entries, e)
				}
			}
		})
	}
}
