package workspace

import (
	"encoding/json"
	"io/fs"

	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
)

// ResolvedContent contains frozen renderer output, never a loader or resolver.
// Ownership/provenance notes and ordered binding deltas survive unchanged.
type ResolvedContent struct{ Rendered []render.Result }
type LaunchBinding = render.Binding
type RenderDiagnostic = render.Diagnostic

// Only this adapter depends on render's result/preparation shape. Plan uses the
// neutral snapshot below; changes to the renderer are validated here first.
type renderSnapshot struct {
	Tree         artifact.Tree
	Root         layout.Root
	RootMode     fs.FileMode
	Binding      LaunchBinding
	Effects      []layout.Row
	Preparations []preparationRequirement
	Diagnostics  []RenderDiagnostic
	SourceDigest artifact.Digest
}
type preparationRequirement struct{ Kind, Destination string }

func snapshotRendered(source render.Result) (renderSnapshot, error) {
	r := copyRecord(source)
	r.Tree.Entries = artifact.CloneEntries(source.Tree.Entries)
	for _, e := range r.Tree.Entries {
		if render.ValidateRelPath(e.Path) != nil || e.Mode&^fs.ModePerm != 0 {
			return renderSnapshot{}, refuse("unsafe_render_entry", "render", Conflict)
		}
	}
	entries, err := artifact.Normalize(r.Tree.Entries)
	if err != nil {
		return renderSnapshot{}, refuse("invalid_artifact_tree", "render", Conflict)
	}
	r.Tree.Entries = entries
	for _, effect := range r.Effects {
		if effect.Form != layout.Link || effect.CredentialPolicy != layout.LinkOnlyNeverWrite || render.ValidateRelPath(effect.Path) != nil || len(effect.Locator.Argv) > 0 || len(effect.Locator.Env) > 0 || effect.Locator.CWD != "" || effect.Locator.RPCProject != "" || effect.Locator.BeforeResume {
			return renderSnapshot{}, refuse("unsupported_render_effect", "effects", Unsupported)
		}
	}
	out := renderSnapshot{Tree: r.Tree, Root: r.Root, RootMode: r.RootMode, Binding: r.Binding, Effects: r.Effects, Diagnostics: r.Diagnostics}
	for _, prep := range r.Preparations {
		switch prep.Kind {
		case "credential-link":
			if prep.Policy != layout.LinkOnlyNeverWrite || render.ValidateRelPath(prep.Destination) != nil {
				return renderSnapshot{}, refuse("unsupported_preparation", "effects", Unsupported)
			}
		case "credential-availability":
			if prep.Destination != "" {
				return renderSnapshot{}, refuse("unsupported_preparation", "effects", Unsupported)
			}
		default:
			return renderSnapshot{}, refuse("unsupported_preparation", "effects", Unsupported)
		}
		out.Preparations = append(out.Preparations, preparationRequirement{prep.Kind, prep.Destination})
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return renderSnapshot{}, refuse("invalid_frozen_input", "render", Conflict)
	}
	out.SourceDigest = artifact.DigestBytes(encoded)
	return out, nil
}

func validateArtifactPath(path string) error { return render.ValidateRelPath(path) }
