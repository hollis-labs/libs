package boot

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/adapters/registry"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func describeProcess(in Input, host HostInputs, project string, tree artifact.Tree, out *Description) error {
	a, err := provider.NewAdapter(in.Dispatch.Provider, in.Dispatch.Mode)
	if err != nil {
		return err
	}
	switch a := a.(type) {
	case *provider.ClaudeAdapter:
		a.Model = in.Dispatch.Model
		a.Bare = in.Dispatch.Variant == "bare"
	case *provider.CodexAdapter:
		a.Model = in.Dispatch.Model
	case *provider.AntigravityAdapter:
		a.Model, a.Effort, a.Agent = in.Dispatch.Model, in.Dispatch.Effort, in.Definition.Name
	case *provider.OpencodeAdapter:
		a.Model, a.Agent = in.Dispatch.Model, in.Definition.Name
	default:
		return errors.New("adapter lacks resolved dispatch fields")
	}
	options := provider.ProjectionOptions{}
	for _, item := range in.Context.Artifacts {
		if item.Field != plan.Skills {
			continue
		}
		pkg := provider.SkillPackage{Name: item.Components["name"]}
		for _, entry := range item.Content.Package.Entries {
			if entry.Kind == artifact.EntryFile {
				pkg.Files = append(pkg.Files, provider.SkillFile{RelPath: entry.Path, Content: entry.Bytes, Mode: entry.Mode})
			}
		}
		if len(pkg.Files) > 0 {
			options.Skills = append(options.Skills, pkg)
		}
	}
	proj, err := a.(provider.ProjectionProvider).ProviderProjection(provider.PlantContext{AgentName: in.Definition.Name}, options)
	if err != nil {
		return err
	}
	for _, tmpl := range proj.Launch.Argv {
		if tmpl.Kind != provider.ArgFile || tmpl.FirstTurnOnly && in.Dispatch.ResumeID != "" {
			continue
		}
		if tmpl.Root != provider.RootBoot && tmpl.Root != provider.RootConfig {
			return errors.New("launch file is outside the canonical boot artifact root")
		}
		found := false
		for _, entry := range tree.Entries {
			if entry.Path == tmpl.RelPath && entry.Kind == artifact.EntryFile {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("required launch artifact %q is absent from canonical render", tmpl.RelPath)
		}
	}
	// Only Launch is consumed. Legacy projection files, native defaults and
	// credential placeholders never enter the workspace artifact plan.
	d, ok := registry.Lookup(string(in.Dispatch.Provider))
	if !ok {
		return errors.New("unknown registry provider")
	}
	posture, err := d.PostureFor(in.Definition.Policy.Profile.Mode, in.Dispatch.Mode)
	if err != nil {
		return err
	}
	b, err := proj.Launch.ResolveTurn(provider.ProjectionRoots{BootRoot: in.Workspace.Boot.Candidate.Path, ConfigRoot: in.Workspace.Boot.Candidate.Path, ProjectRoot: project, CWD: project, StateRoot: in.Workspace.Home.Root.Path}, provider.TurnInput{Prompt: in.Dispatch.Prompt, SystemPrompt: in.Dispatch.SystemPrompt, ResumeID: in.Dispatch.ResumeID}, posture.Args)
	if err != nil {
		return err
	}
	if in.Workspace.CWD.Child != "" && in.Workspace.CWD.Child != b.CWD {
		return failure(PhaseProject, "unsupported_child_cwd", errors.New("explicit child CWD differs from the provider launch convention"))
	}
	out.Executable, out.Argv, out.CWD = proj.Launch.Executable, b.Argv, b.CWD
	out.Environment = nil
	derived := Provenance{"harness/provider-launch", "harness-provider-conventions-v1"}
	for _, delta := range b.Env {
		out.Environment = append(out.Environment, EnvironmentEntry{delta, derived})
	}
	keys := make([]string, 0, len(posture.Env))
	for k := range posture.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Environment = append(out.Environment, EnvironmentEntry{provider.EnvDelta{Name: k, Value: posture.Env[k], Operation: provider.EnvSet, Precedence: provider.EnvProviderWins}, Provenance{"harness/permission-binding", in.Definition.Policy.Profile.Version}})
	}
	for _, entry := range host.Environment {
		if !validOrigin(entry.Provenance) || !environmentName.MatchString(entry.Delta.Name) || strings.ContainsRune(entry.Delta.Value, 0) {
			return errors.New("invalid host environment or missing provenance")
		}
		switch entry.Delta.Operation {
		case provider.EnvSet, provider.EnvUnset:
		default:
			return errors.New("host environment requires explicit set or unset")
		}
		if entry.Delta.Precedence != provider.EnvCallerWins && entry.Delta.Precedence != provider.EnvProviderWins {
			return errors.New("host environment requires explicit precedence")
		}
		for _, prior := range out.Environment {
			if prior.Delta.Name == entry.Delta.Name {
				return fmt.Errorf("environment collision for %s; host cannot replace runtime policy or discovery", entry.Delta.Name)
			}
		}
		out.Environment = append(out.Environment, entry)
	}
	out.Delivery = Delivery{Kind: "argv", Prompt: in.Dispatch.Prompt, SystemPrompt: in.Dispatch.SystemPrompt, ResumeID: in.Dispatch.ResumeID}
	switch in.Dispatch.Mode {
	case runtimes.ModeStreamingStdio:
		// The existing Claude input format is a user message JSON line. Nothing
		// is written to stdin here; a caller owns delivery and spawn lifecycle.
		frame := struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}{Type: "user"}
		frame.Message.Role, frame.Message.Content = "user", in.Dispatch.Prompt
		out.Delivery.Kind = "stdin-json-line"
		out.Delivery.Stdin, err = json.Marshal(frame)
		if err != nil {
			return err
		}
		out.Delivery.Stdin = append(out.Delivery.Stdin, '\n')
	case runtimes.ModePTY:
		out.Delivery.Kind = "stdin-text"
		out.Delivery.Stdin = []byte(in.Dispatch.Prompt)
	case runtimes.ModeJSONRPCStdio, runtimes.ModeHTTPSSE:
		out.Delivery.Kind = "rpc-turn"
	}
	return nil
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
