package agentlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
)

func TestMaterializerAuthorityBeforeRenderer(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "candidate")
			renderer := &harnessRenderer{}
			var auth ArtifactAuthorizer
			authorizations, records, closes := 0, 0, 0
			if kind == "invalid" {
				base := fixtureAuthorization(t)
				auth = func(ctx context.Context, path string) (ArtifactAuthority, error) {
					authorizations++
					a, e := base(ctx, path)
					a.Ports.ReceiptStore = &authorityOrderStore{a.Ports.ReceiptStore, &records}
					closePorts := a.Close
					a.Close = func() error { closes++; return closePorts() }
					a.Inactive = false
					return a, e
				}
			}
			ctx := context.Background()
			if kind == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
				auth = func(context.Context, string) (ArtifactAuthority, error) {
					authorizations++
					return ArtifactAuthority{}, nil
				}
			}
			_, err := NewDefaultMaterializer(MaterializerOptions{Authorize: auth}).Populate(ctx, root, MaterializeRequest{Spec: harnessSpec("claude")}, renderer)
			if err == nil {
				t.Fatal("expected authority refusal")
			}
			if _, e := os.Stat(root); !os.IsNotExist(e) {
				t.Fatal("candidate mutated", e)
			}
			if len(renderer.calls) != 0 {
				t.Fatalf("renderer ran before authority refusal: kind=%s calls=%v err=%v", kind, renderer.calls, err)
			}
			if records != 0 || kind == "invalid" && closes != 1 || kind == "cancelled" && authorizations != 0 {
				t.Fatal("authority/receipt lifecycle crossed refusal", authorizations, records, closes)
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

type authorityOrderRenderer func(context.Context, ContractRenderRequest) (string, error)

func (f authorityOrderRenderer) RenderContractObject(ctx context.Context, r ContractRenderRequest) (string, error) {
	return f(ctx, r)
}

func TestMaterializerClosesOneResolvedAuthority(t *testing.T) {
	for _, outcome := range []string{"success", "render error", "cancel during render", "cancel during injection"} {
		t.Run(outcome, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "candidate")
			resolve := fixtureAuthorization(t)
			resolves, closes, calls, records := 0, 0, 0, 0
			authorize := func(ctx context.Context, path string) (ArtifactAuthority, error) {
				resolves++
				a, err := resolve(ctx, path)
				closePorts := a.Close
				a.Close = func() error { closes++; return closePorts() }
				a.Ports.ReceiptStore = &authorityOrderStore{a.Ports.ReceiptStore, &records}
				return a, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			renderer := authorityOrderRenderer(func(context.Context, ContractRenderRequest) (string, error) {
				calls++
				if outcome == "render error" {
					return "", errors.New("fixture render failure")
				}
				if outcome == "cancel during render" || outcome == "cancel during injection" {
					cancel()
				}
				return "fixture", nil
			})
			spec := harnessSpec("claude")
			if outcome == "cancel during injection" {
				spec.Files = nil
				for i := range spec.Injections {
					spec.Injections[i].Object = ContractObject{Kind: ContractObjectSlot, Ref: "injection-fixture"}
				}
			}
			_, err := NewDefaultMaterializer(MaterializerOptions{Authorize: authorize}).Populate(ctx, root, MaterializeRequest{Spec: spec}, renderer)
			if resolves != 1 || closes != 1 {
				t.Fatal("authority ownership changed", resolves, closes, err)
			}
			if outcome == "success" {
				if err != nil || records == 0 || calls == 0 {
					t.Fatal("valid authority refused", err)
				}
			} else {
				if err == nil || records != 0 || calls != 1 {
					t.Fatal("render failure crossed boundary", calls, records, err)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("failure mutated candidate", err)
				}
			}
		})
	}
}

func TestMaterializerEmptySelectionDoesNotResolveAuthority(t *testing.T) {
	spec := harnessSpec("claude")
	spec.Files = nil
	spec.Injections = nil
	root := filepath.Join(t.TempDir(), "candidate")
	called := false
	authorize := func(context.Context, string) (ArtifactAuthority, error) {
		called = true
		return ArtifactAuthority{}, nil
	}
	result, err := NewDefaultMaterializer(MaterializerOptions{Authorize: authorize}).Populate(context.Background(), root, MaterializeRequest{Spec: spec}, &harnessRenderer{})
	if err != nil || result == nil || called {
		t.Fatal("empty materialization changed", called, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("empty selection mutated candidate", err)
	}
}
