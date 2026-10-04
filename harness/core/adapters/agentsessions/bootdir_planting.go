package agentsessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// ArtifactPreparationError exposes trusted root accounting without cleanup.
type ArtifactPreparationError struct {
	Result workspace.ApplyResult
	Err    error
}

func (e *ArtifactPreparationError) Error() string {
	return "agentsessions: artifact preparation: " + e.Err.Error()
}
func (e *ArtifactPreparationError) Unwrap() error { return e.Err }

// preparePlant renders a pure BootDirSpec only after explicit host authority
// preflight, then applies through the sole workspace engine. Failures preserve
// the original options/adapter and expose retained root evidence; callbacks and
// binding clones follow verified commit. It creates no implicit root or parent.
func preparePlant(ctx context.Context, opts StartOptions, adapter provider.CLIAdapter, runtimeID string) (bootDir string, planted StartOptions, sessionAdapter provider.CLIAdapter, err error) {
	planted, sessionAdapter = opts, adapter
	if !opts.AutoPlantBootDir {
		return "", planted, sessionAdapter, nil
	}
	bp, ok := adapter.(provider.BootDirProvider)
	if !ok {
		return "", planted, sessionAdapter, nil
	}
	spec := bp.BootDirSpec()
	if len(spec.PlantedFiles) == 0 {
		return "", planted, sessionAdapter, nil
	}
	if opts.ArtifactAuthorization == nil || opts.ArtifactRoot == "" {
		return "", opts, adapter, &workspace.Refusal{Code: "automatic_plant_authority_pending", Concern: "authority", Status: workspace.Unsupported}
	}
	if ctx == nil {
		return "", opts, adapter, &workspace.Refusal{Code: "invalid_artifact_authority", Concern: "authority", Status: workspace.Unsupported}
	}
	if err := ctx.Err(); err != nil {
		return "", opts, adapter, err
	}
	authority, err := opts.ArtifactAuthorization(ctx, opts.ArtifactRoot)
	if err != nil {
		return "", opts, adapter, errors.Join(&workspace.Refusal{Code: "invalid_artifact_authority", Concern: "authority", Status: workspace.Unsupported}, err)
	}
	closeInvalid := func(err error) error {
		if authority.Close != nil {
			return errors.Join(err, authority.Close())
		}
		return err
	}
	if err := agentlaunch.ValidateArtifactAuthority(ctx, authority, opts.ArtifactRoot); err != nil {
		return "", opts, adapter, closeInvalid(err)
	}
	if opts.PlantContext.LegacyAllowHostEffects {
		return "", opts, adapter, closeInvalid(&workspace.Refusal{Code: "legacy_render_effects_unsupported", Concern: "effects", Status: workspace.Unsupported})
	}
	dir, projectDir := authority.Input.Root.Path, opts.Workdir
	plantCtx := opts.PlantContext
	plantCtx.SystemPrompt = opts.BootPrompt
	plantCtx.BootContent = opts.BootContent
	if plantCtx.BootContent == "" {
		plantCtx.BootContent = opts.BootPrompt
	}
	plantCtx.ProjectDir, plantCtx.BootDir = projectDir, dir
	entries := []artifact.Entry{}
	for _, pf := range spec.PlantedFiles {
		if err := ctx.Err(); err != nil {
			return "", opts, adapter, closeInvalid(err)
		}
		if pf.Render == nil {
			continue
		}
		content, err := pf.Render(plantCtx)
		if err != nil {
			return "", opts, adapter, closeInvalid(fmt.Errorf("agentsessions: plant %s: render: %w", pf.RelPath, err))
		}
		mode := pf.Mode
		if mode == 0 {
			mode = plantedFileMode(pf)
		}
		entries = append(entries, artifact.Entry{Path: pf.RelPath, Kind: artifact.EntryFile, Mode: mode, Bytes: []byte(content), Ownership: artifact.Ownership{EntryID: "agentsessions.bootdir:" + pf.RelPath, GroupID: "agentsessions.bootdir"}, Provenance: artifact.Provenance{Source: "agentsessions.bootdir"}})
	}
	result, err := agentlaunch.MaterializeArtifactsResult(ctx, agentlaunch.ArtifactMaterializationRequest{TargetRoot: dir, Roots: agentlaunch.ExecutionRoots{BootRoot: dir, ProjectRoot: projectDir}, Artifacts: artifact.Tree{Entries: entries}, Authorize: func(context.Context, string) (agentlaunch.ArtifactAuthority, error) { return authority, nil }})
	if err != nil {
		return "", opts, adapter, &ArtifactPreparationError{Result: result, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return "", opts, adapter, &ArtifactPreparationError{Result: result, Err: err}
	}
	envAmend := substituteTemplates(spec.EnvAmendments, dir, projectDir)
	projectDirArg := substituteArgTokens(spec.ProjectDirArg, dir, projectDir)
	sessionAdapter, projectDirArg = applyBareInjection(adapter, dir, projectDir, projectDirArg)
	sessionAdapter, projectDirArg = applyCodexProjectDir(sessionAdapter, projectDir, projectDirArg)
	snapshot := result.Clone()
	planted.artifactPreparation = &snapshot
	planted.Workdir = spec.SpawnWorkdir(dir, projectDir)
	if len(envAmend) > 0 {
		planted.Env = append(append([]string(nil), opts.Env...), envAmend...)
	}
	if len(projectDirArg) > 0 {
		planted.ExtraArgs = append(append([]string(nil), opts.ExtraArgs...), projectDirArg...)
	}
	if opts.OnArtifactPrepared != nil {
		opts.OnArtifactPrepared(result.Clone())
	}
	if opts.OnBootDirPlanted != nil {
		opts.OnBootDirPlanted(dir)
	}
	return dir, planted, sessionAdapter, nil
}

func retainPreparationOnStartFailure(opts StartOptions, err error) error {
	if err == nil || opts.artifactPreparation == nil {
		return err
	}
	result := opts.artifactPreparation.Clone()
	result.Status = workspace.Partial
	result.Diagnostics = append(result.Diagnostics, workspace.Diagnostic{Code: "session_start_failed", Concern: "launch", Status: workspace.Partial})
	return &ArtifactPreparationError{Result: result, Err: err}
}

// plantedFileMode picks the file mode for a planted file. .mcp.json and
// any *settings.json get 0o600 because they conventionally carry tokens
// or MCP loopback URLs that an attacker with read access could probe; all
// other files default to 0o644. Matches the convention the Mux/Nanite/
// Clockwork per-app planters used before absorption.
func plantedFileMode(pf provider.PlantedFile) os.FileMode {
	if pf.RelPath == ".mcp.json" || strings.HasSuffix(pf.RelPath, "settings.json") {
		return 0o600
	}
	return 0o644
}

// substituteTemplates replaces {{.BootDir}} / {{.ProjectDir}} in each
// "KEY=VALUE" env amendment. Nil/empty in → nil out so callers can
// short-circuit the append.
func substituteTemplates(in []string, bootDir, projectDir string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ReplaceAll(s, "{{.BootDir}}", bootDir)
		s = strings.ReplaceAll(s, "{{.ProjectDir}}", projectDir)
		out = append(out, s)
	}
	return out
}

// substituteArgTokens resolves a BootDirSpec.ProjectDirArg template into
// a space-tokenized argv slice. Empty template or empty projectDir
// returns nil — the spec convention is that ProjectDirArg only fires
// when there is a project to point at.
func substituteArgTokens(template, bootDir, projectDir string) []string {
	if template == "" || projectDir == "" {
		return nil
	}
	parts := strings.Fields(template)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ReplaceAll(part, "{{.BootDir}}", bootDir)
		part = strings.ReplaceAll(part, "{{.ProjectDir}}", projectDir)
		out = append(out, part)
	}
	return out
}

// applyBareInjection mutates a per-session clone of *provider.ClaudeAdapter
// when the adapter declares bare-mode, threading the planted paths into
// the adapter's MCPConfigPath / AppendSystemPromptFile / SettingsPath /
// ProjectDir fields. Returns the (possibly cloned) adapter and an updated
// projectDirArg slice (bare-mode bakes --add-dir into BuildArgs via the
// injected fields, so the runtime must NOT also splice it via ExtraArgs).
//
// Non-Claude adapters or non-bare ClaudeAdapters are returned unchanged
// along with the unaltered projectDirArg.
func applyBareInjection(adapter provider.CLIAdapter, bootDir, projectDir string, projectDirArg []string) (provider.CLIAdapter, []string) {
	claude, ok := adapter.(*provider.ClaudeAdapter)
	if !ok || !claude.Bare {
		return adapter, projectDirArg
	}
	clone := *claude
	inj := clone.BareInjectionPaths(bootDir, projectDir)
	clone.MCPConfigPath = inj.MCPConfigPath
	clone.AppendSystemPromptFile = inj.AppendSystemPromptFile
	clone.SettingsPath = inj.SettingsPath
	clone.ProjectDir = inj.ProjectDir
	// Bare BuildArgs already emits --add-dir for clone.ProjectDir; emitting
	// it again via ExtraArgs would double-add the flag.
	return &clone, nil
}

// applyCodexProjectDir gives a codex exec adapter the project through its own
// ProjectDir field on a per-session clone, instead of splicing BootDirSpec's
// `--cd <project>` in through ExtraArgs. go-providers v0.41.0 resumes a codex
// exec thread with `exec … --cd <project> resume <id> -- <prompt>`, and codex
// takes --cd only in front of the resume subcommand: spliced before "--" it
// landed after `resume <id>`, where codex-cli 0.159.2 refuses it ("unexpected
// argument '--cd' found"), so every auto-planted codex exec session failed
// its second turn. The field puts --cd at the launch convention's slot, before
// `resume`. A first turn's argv is unchanged.
//
// App-server adapters, other adapters, and a spec with no project-dir
// argument are returned unchanged along with projectDirArg.
func applyCodexProjectDir(adapter provider.CLIAdapter, projectDir string, projectDirArg []string) (provider.CLIAdapter, []string) {
	codex, ok := adapter.(*provider.CodexAdapter)
	if !ok || codex.Mode == "app-server" || len(projectDirArg) == 0 {
		return adapter, projectDirArg
	}
	clone := *codex
	clone.ProjectDir = projectDir
	return &clone, nil
}

// sanitizeBootDirID restricts a runtime ID to the safe character set for
// filesystem path components. Same shape as the per-app planters used
// before absorption.
func sanitizeBootDirID(s string) string {
	if s == "" {
		return "session"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}
