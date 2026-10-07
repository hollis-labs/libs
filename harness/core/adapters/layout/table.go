package layout

import (
	"strings"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
)

// compatibilityTable is a mechanical view of the sole authored plan table.
// It contains no authored paths, permissions or launch-token policy.
func compatibilityTable() []Entry {
	var out []Entry
	seen := map[string]bool{}
	add := func(e Entry) {
		key := strings.Join([]string{string(e.Provider), string(e.Mode), string(e.Variant), string(e.Concern), string(e.Root), e.Rel, e.Flag}, "\x00")
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	for _, row := range plan.Table() {
		if row.Layer != plan.Boot || row.Capability != plan.Supported {
			continue
		}
		var concern Concern
		switch row.Field {
		case plan.Instructions:
			concern = Instructions
		case plan.Kickoff:
			concern = Boot
		case plan.Settings, plan.PlantingPlugin:
			concern = NativeConfig
		case plan.MCP:
			concern = MCP
		case plan.Skills:
			concern = Skills
		case plan.Credentials:
			concern = Auth
		default:
			continue
		}
		e := Entry{Provider: row.Provider, Mode: row.Mode, Variant: Variant(row.Variant), Concern: concern,
			Root: Root(row.Root), Rel: row.Path, FileMode: row.CompatibilityModeBits,
			CWD: Root(row.Locator.CWD), Note: row.Evidence.Note}
		// A mirror is explicitly named in the same canonical MCP row.
		if row.CompatibilityMCPPath != "" {
			e.Rel = row.CompatibilityMCPPath
		}
		if row.Field == plan.Skills {
			const suffix = "/{name}/SKILL.md"
			if row.Form != plan.Package || !strings.HasSuffix(row.Path, suffix) {
				panic("layout: skill placement lacks declared package suffix")
			}
			e.Rel = strings.TrimSuffix(row.Path, suffix)
			e.Form = FormDir
		}
		if strings.Contains(row.Evidence.Reference, "/harness-discovery/") && len(row.Evidence.Observations) > 0 {
			e.Probe = append([]string(nil), row.Evidence.Observations...)
		} else {
			e.Unprobed = row.Evidence.Reference
		}
		for key, value := range row.Locator.Env {
			if e.Env == nil {
				e.Env = map[string]string{}
			}
			e.Env[key] = string(value)
		}
		for i, token := range row.Locator.Argv {
			if i == 0 {
				continue
			}
			if token == "{path}" || row.Field == plan.Skills && token == "{B}" {
				e.Flag = row.Locator.Argv[i-1]
			}
		}
		add(e)
		// Project placement is a token pair, never an RPC project parameter.
		for i, token := range row.Locator.Argv {
			if i > 0 && token == "{P}" {
				project := e.clone()
				project.Concern, project.Root, project.Rel, project.Form = ProjectDir, RootProject, "", ""
				project.FileMode = 0
				project.Flag = row.Locator.Argv[i-1]
				add(project)
			}
		}
		if row.Locator.RPCProject != "" && len(row.Locator.Argv) > 0 && !strings.HasPrefix(row.Locator.Argv[0], "-") {
			runtime := e.clone()
			runtime.Concern, runtime.Root, runtime.Rel, runtime.Form = Runtime, RootProject, "", ""
			runtime.FileMode = 0
			runtime.Flag = row.Locator.Argv[0]
			add(runtime)
		}
	}
	return out
}
