package workspace

import (
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"path"
	"slices"
	"strings"
)

func validateBinding(r renderSnapshot, roots map[layout.Root]string) error {
	rows, err := layout.For(r.Provider, r.Layer, r.Mode, r.Variant)
	if err != nil {
		return refuse(CodeInvalidRenderContext, "render", Conflict)
	}
	agent := r.Binding.RPCProject["agent"]
	for i, tok := range r.Binding.Argv {
		if tok == "--agent" && i+1 < len(r.Binding.Argv) {
			if agent != "" && agent != r.Binding.Argv[i+1] {
				return refuse(CodeInvalidRenderBinding, "render", Conflict)
			}
			agent = r.Binding.Argv[i+1]
		}
	}
	if agent != "" && render.ValidateComponent(agent) != nil {
		return refuse(CodeInvalidRenderBinding, "render", Conflict)
	}
	env := map[string]string{}
	cwds := map[string]bool{}
	rpc := map[string]string{}
	groups := map[string]bool{}
	beforeResume := false
	for _, row := range rows {
		if row.Capability != layout.Supported || row.Form == layout.Link || row.Form == layout.RuntimeBinding {
			continue
		}
		rowPath := row.Path
		if strings.Contains(rowPath, "{agent}") {
			if agent != "" {
				rowPath = strings.ReplaceAll(rowPath, "{agent}", agent)
			}
		}
		for name, root := range row.Locator.Env {
			if roots[root] != "" {
				env[name] = roots[root]
			}
		}
		if roots[row.Locator.CWD] != "" {
			cwds[roots[row.Locator.CWD]] = true
		}
		beforeResume = beforeResume || row.Locator.BeforeResume
		switch row.Locator.RPCProject {
		case "thread.cwd":
			rpc["thread.cwd"] = roots[layout.RootProject]
		case "directory/agent":
			rpc["directory"] = roots[layout.RootProject]
			rpc["agent"] = agent
		}
		args := row.Locator.Argv
		for i := 0; i < len(args); i++ {
			group := []string{args[i]}
			if strings.HasPrefix(args[i], "--") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				group = append(group, args[i])
			}
			valid := true
			for j, token := range group {
				switch token {
				case "{B}":
					group[j] = roots[layout.RootBoot]
				case "{P}":
					group[j] = roots[layout.RootProject]
				case "{H}":
					group[j] = roots[layout.RootHome]
				case "{path}":
					if strings.ContainsAny(rowPath, "{}") {
						valid = false
					}
					group[j] = path.Join(roots[row.Root], rowPath)
				case "{agent}":
					group[j] = agent
				}
				if group[j] == "" {
					valid = false
				}
			}
			if valid {
				groups[strings.Join(group, "\x00")] = true
			}
		}
		// Resolve permits Claude's explicit exclusive-MCP request to append this
		// backstop. It is not an arbitrary launch flag.
		if row.Field == layout.MCP && row.ExclusiveMCP == layout.Supported && len(row.Locator.Argv) > 0 {
			groups["--strict-mcp-config"] = true
		}
	}
	if r.Binding.CWD != "" && !cwds[r.Binding.CWD] {
		return refuse(CodeInvalidRenderBinding, "render", Conflict)
	}
	for name, value := range r.Binding.Environment {
		if name == "" || credentialEnvironmentName(name) || env[name] == "" || value != env[name] {
			return refuse(CodeInvalidRenderBinding, "render", Conflict)
		}
	}
	for name, value := range r.Binding.RPCProject {
		if value == "" || rpc[name] == "" || value != rpc[name] {
			return refuse(CodeInvalidRenderBinding, "render", Conflict)
		}
	}
	if _, ok := r.Binding.RPCProject["agent"]; ok {
		if r.Binding.RPCProject["directory"] == "" {
			return refuse(CodeInvalidRenderBinding, "render", Conflict)
		}
	}
	if _, ok := r.Binding.RPCProject["directory"]; ok {
		if agent == "" {
			return refuse(CodeInvalidRenderBinding, "render", Conflict)
		}
	}
	for i := 0; i < len(r.Binding.Argv); i++ {
		group := []string{r.Binding.Argv[i]}
		if strings.HasPrefix(group[0], "--") && i+1 < len(r.Binding.Argv) && !strings.HasPrefix(r.Binding.Argv[i+1], "--") {
			i++
			group = append(group, r.Binding.Argv[i])
		}
		if !groups[strings.Join(group, "\x00")] {
			return refuse(CodeInvalidRenderBinding, "render", Conflict)
		}
	}
	if r.Binding.BeforeResume && !beforeResume {
		return refuse(CodeInvalidRenderBinding, "render", Conflict)
	}
	if p := r.Binding.Posture; p != nil {
		if p.Provider != r.Provider || p.Mapper != "adapters/registry.Descriptor.PostureFor" || !slices.Contains([]permission.Mode{permission.ModeDefault, permission.ModePlan, permission.ModeAcceptEdits, permission.ModeYolo}, p.Posture) {
			return refuse(CodeInvalidRenderBinding, "render", Conflict)
		}
	}
	return nil
}
