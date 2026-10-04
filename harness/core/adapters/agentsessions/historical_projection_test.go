package agentsessions

import (
	"fmt"
	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/workspace/goldens"
	"path/filepath"
	"sort"
)

// historicalSessionProjection renders archive evidence in memory only. The
// archive deliberately records the old ignored PlantedFile.Mode behavior and
// parent modes; none of those values authorizes active planting or cleanup.
func historicalSessionProjection(opts StartOptions, a provider.CLIAdapter, dir string) ([]goldens.Entry, StartOptions, provider.CLIAdapter, error) {
	spec := a.(provider.BootDirProvider).BootDirSpec()
	pc := opts.PlantContext
	pc.SystemPrompt = opts.BootPrompt
	pc.BootContent = opts.BootContent
	if pc.BootContent == "" {
		pc.BootContent = opts.BootPrompt
	}
	pc.ProjectDir, pc.BootDir = opts.Workdir, dir
	entries := map[string]goldens.Entry{".": {Path: ".", Kind: "directory", Mode: "0700"}}
	for _, pf := range spec.PlantedFiles {
		if pf.Render == nil {
			continue
		}
		content, err := pf.Render(pc)
		if err != nil {
			return []goldens.Entry{{Path: ".", Kind: "directory", Mode: "0750"}}, opts, a, fmt.Errorf("agentsessions: plant %s: render: %w", pf.RelPath, err)
		}
		entries[pf.RelPath] = goldens.Entry{Path: pf.RelPath, Kind: "file", Mode: fmt.Sprintf("%04o", plantedFileMode(pf)), Content: content}
		for parent := filepath.ToSlash(filepath.Dir(pf.RelPath)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			entries[parent] = goldens.Entry{Path: parent, Kind: "directory", Mode: "0750"}
		}
	}
	env := substituteTemplates(spec.EnvAmendments, dir, opts.Workdir)
	args := substituteArgTokens(spec.ProjectDirArg, dir, opts.Workdir)
	sa, args := applyBareInjection(a, dir, opts.Workdir, args)
	sa, args = applyCodexProjectDir(sa, opts.Workdir, args)
	planted := opts
	planted.Workdir = spec.SpawnWorkdir(dir, opts.Workdir)
	if len(env) > 0 {
		planted.Env = append(append([]string(nil), opts.Env...), env...)
	}
	if len(args) > 0 {
		planted.ExtraArgs = append(append([]string(nil), opts.ExtraArgs...), args...)
	}
	out := []goldens.Entry{}
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, planted, sa, nil
}
