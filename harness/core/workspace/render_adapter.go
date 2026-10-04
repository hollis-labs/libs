package workspace

import (
	"encoding/json"
	"io/fs"
	"strings"

	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// ResolvedContent contains frozen renderer output, never a loader or resolver.
// Ownership/provenance notes and ordered binding deltas survive unchanged.
type ResolvedContent struct {
	Rendered []render.Result
	// Roots must be the exact Request.Roots used for every Rendered result.
	// Bindings already contain these paths; Plan never retargets them.
	Roots map[layout.Root]string
}
type LaunchBinding = render.Binding
type RenderDiagnostic = render.Diagnostic

// Only this adapter depends on render's result/preparation shape. Plan uses the
// neutral snapshot below; changes to the renderer are validated here first.
type renderSnapshot struct {
	Provider     runtimes.ID
	Layer        layout.Layer
	Mode         runtimes.Mode
	Variant      layout.Variant
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
	rows, err := layout.For(r.Provider, r.Layer, r.Mode, r.Variant)
	if err != nil || (r.Layer == layout.Boot && r.Root != layout.RootBoot) || (r.Layer == layout.Installed && r.Root != layout.RootHome) {
		return renderSnapshot{}, refuse("invalid_render_context", "render", Conflict)
	}
	for _, d := range r.Diagnostics {
		if d.Code == "" || (d.Class != render.ClassInformational && d.Class != render.ClassOmission) || d.Provider != "" && d.Provider != r.Provider || d.Mode != "" && d.Mode != r.Mode {
			return renderSnapshot{}, refuse("invalid_render_diagnostic", "render", Conflict)
		}
	}
	for _, e := range r.Tree.Entries {
		if e.Kind != artifact.EntryFile {
			continue
		}
		for _, row := range rows {
			if !strings.EqualFold(row.Path, e.Path) {
				continue
			}
			switch row.Field {
			case layout.Settings, layout.Permissions, layout.Hooks, layout.MCP, layout.PlantingPlugin:
				if row.Form == layout.File || row.Form == layout.Slot {
					if _, err := render.ParseOwnershipNote(e.Provenance.Note); err != nil {
						return renderSnapshot{}, refuse("invalid_render_ownership", "render", Conflict)
					}
				}
			}
		}
	}
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
	out := renderSnapshot{Provider: r.Provider, Layer: r.Layer, Mode: r.Mode, Variant: r.Variant, Tree: r.Tree, Root: r.Root, RootMode: r.RootMode, Binding: r.Binding, Effects: r.Effects, Diagnostics: r.Diagnostics}
	for _, prep := range r.Preparations {
		if prep.Provider != r.Provider {
			return renderSnapshot{}, refuse("unsupported_preparation", "effects", Unsupported)
		}
		switch prep.Kind {
		case render.PreparationCredentialLink:
			if prep.Policy != layout.LinkOnlyNeverWrite || render.ValidateRelPath(prep.Destination) != nil {
				return renderSnapshot{}, refuse("unsupported_preparation", "effects", Unsupported)
			}
		case render.PreparationCredentialAvailability:
			if prep.Destination != "" {
				return renderSnapshot{}, refuse("unsupported_preparation", "effects", Unsupported)
			}
		default:
			return renderSnapshot{}, refuse("unsupported_preparation", "effects", Unsupported)
		}
		out.Preparations = append(out.Preparations, preparationRequirement{string(prep.Kind), prep.Destination})
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return renderSnapshot{}, refuse("invalid_frozen_input", "render", Conflict)
	}
	out.SourceDigest = artifact.DigestBytes(encoded)
	return out, nil
}

func validateArtifactPath(path string) error { return render.ValidateRelPath(path) }

func isCredentialDestination(p string) bool { return render.IsCredentialDestination(p) }

func validateRenderRoots(r renderSnapshot, roots map[layout.Root]string, target RootRef, resolved []RootRef) error {
	if roots[r.Root] != target.Path {
		return refuse("render_root_mismatch", "render", Conflict)
	}
	for logical, path := range roots {
		if logical != layout.RootBoot && logical != layout.RootHome && logical != layout.RootProject {
			return refuse("unresolved_render_root", "render", Conflict)
		}
		found := false
		for _, ref := range resolved {
			if ref.Path == path {
				found = true
				break
			}
		}
		if !found {
			return refuse("unresolved_render_root", "render", Conflict)
		}
	}
	return nil
}
