package workspace_test

import (
	"github.com/hollis-labs/substrate/harness/workspace"
	"path/filepath"
	"testing"
)

func TestResolvedScopePreservesExplicitLifetimeAndDetachedInputs(t *testing.T) {
	for _, continuity := range []workspace.Continuity{workspace.Durable, workspace.Ephemeral} {
		s, _, r, _ := planInputs(t)
		s.Home.Continuity = continuity
		got, err := workspace.ResolveScope(s, r, r.LockRoot)
		if err != nil || !got.Valid() {
			t.Fatalf("scope: %v", err)
		}
		r.Roots[0].Owner = "changed"
		s.Boot.RowIDs = []string{"changed"}
		copy := got.Resources()
		copy.Roots[0].Path = "changed"
		if got.Resources().Roots[0].Path == "changed" || got.Resources().Roots[0].Owner == "changed" || len(got.Spec().Boot.RowIDs) != 0 || got.Spec().Home.Continuity != continuity {
			t.Fatal("scope escaped detached inputs")
		}
		if got.Spec().Identity.EncodedKey != s.Identity.EncodedKey {
			t.Fatal("encoded key encoded again")
		}
	}
}
func TestResolvedScopeRefusesInventedIdentityAndControl(t *testing.T) {
	for _, kind := range []string{"encoded-again", "missing-host-root", "control-inside", "lock-alias", "current-shape"} {
		t.Run(kind, func(t *testing.T) {
			s, _, r, _ := planInputs(t)
			control := r.LockRoot
			switch kind {
			case "encoded-again":
				s.Identity.AgentURN = s.Identity.EncodedKey
			case "missing-host-root":
				r.Roots = r.Roots[1:]
			case "control-inside":
				control.Path = filepath.Join(s.Boot.IdentityRoot.Path, "control")
			case "lock-alias":
				r.LockNamespace = filepath.Join(r.LockNamespace, "other")
			case "current-shape":
				s.Boot.Current.Path = filepath.Join(s.Boot.IdentityRoot.Path, "selector")
			}
			if _, err := workspace.ResolveScope(s, r, control); err == nil {
				t.Fatal("unsafe structural scope accepted")
			}
		})
	}
	if (workspace.Scope{}).Valid() {
		t.Fatal("zero scope valid")
	}
}
