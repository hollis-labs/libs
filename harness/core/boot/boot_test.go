package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// These ports are synthetic authority in private fixture roots. The managed
// artifacts and manifest are written only by the real workspace engine.
type fixture struct {
	now           time.Time
	observed      workspace.Observations
	calls         []string
	failDirectory string
	failPhase     workspace.Phase
	validate      func()
	record        func(workspace.Receipt)
	receipts      []workspace.Receipt
}

func (f *fixture) Now() time.Time { f.calls = append(f.calls, "clock"); return f.now }
func (f *fixture) Validate(context.Context, workspace.Spec, workspace.Resources) error {
	f.calls = append(f.calls, "validate")
	if f.validate != nil {
		f.validate()
	}
	return nil
}
func (f *fixture) EnsureOwnedDirectory(_ context.Context, r workspace.RootRef, mode fs.FileMode) error {
	f.calls = append(f.calls, "directory:"+r.ID)
	if r.ID == f.failDirectory {
		return errors.New("fixture directory refusal")
	}
	err := os.Mkdir(r.Path, mode)
	if os.IsExist(err) {
		return nil
	}
	return err
}
func (f *fixture) Acquire(_ context.Context, k workspace.LockKey) (workspace.HeldLock, error) {
	f.calls = append(f.calls, "lock:"+k.CanonicalID)
	return fixtureLock{f}, nil
}

type fixtureLock struct{ f *fixture }

func (l fixtureLock) Release() error { l.f.calls = append(l.f.calls, "release"); return nil }
func (f *fixture) Observe(context.Context, workspace.Resources) (workspace.Observations, error) {
	f.calls = append(f.calls, "observe")
	return detach(f.observed)
}
func (f *fixture) Record(_ context.Context, r workspace.Receipt) error {
	f.calls = append(f.calls, "record:"+string(r.Phase))
	if f.record != nil {
		f.record(r)
	}
	if r.Phase == f.failPhase {
		return errors.New("fixture receipt refusal")
	}
	r, err := detach(r)
	if err != nil {
		return err
	}
	f.receipts = append(f.receipts, r)
	return nil
}

func inputFixture(t *testing.T) (Input, HostInputs, *fixture) {
	t.Helper()
	base := t.TempDir()
	key, err := bootkey.Encode("urn:fixture:agent:boot")
	if err != nil {
		t.Fatal(err)
	}
	root := func(id, path string) workspace.RootRef {
		return workspace.RootRef{ID: id, Path: filepath.Join(base, path), AllowedBase: base, Owner: "fixture-owner", Provenance: "fixture"}
	}
	home, identity := root("home", "home"), root("identity", key)
	candidate, current := root("candidate", filepath.Join(key, "candidate")), root("current", filepath.Join(key, "current"))
	control := root("control", "control")
	if err := os.Mkdir(control.Path, 0700); err != nil {
		t.Fatal(err)
	}
	digest := artifact.DigestBytes([]byte("fixture definition"))
	profile, err := permission.BindProfile("plan", permission.Ceiling{Modes: []permission.Mode{permission.ModePlan}})
	if err != nil {
		t.Fatal(err)
	}
	origin := Provenance{"fixture-resolver", "revision-1"}
	spec := workspace.Spec{SchemaVersion: workspace.SchemaVersion, OperationID: "fixture-prepare", Operation: workspace.Prepare,
		Identity: workspace.IdentitySpec{AgentURN: "urn:fixture:agent:boot", EncodedKey: key, Session: "session", DefinitionRevision: "revision-1", SemanticDigest: digest, ArtifactDigest: digest, DependencyDigest: digest, Fence: workspace.ResourceRef{ID: "fence", Revision: "1"}},
		Home:     workspace.HomeSpec{Root: home, Layout: workspace.FullHome, Continuity: workspace.Durable, Retention: workspace.Keep},
		Boot:     workspace.BootSpec{IdentityRoot: identity, Current: current, Candidate: candidate, Retention: workspace.RetainForRecovery},
		CWD:      workspace.CWDSpec{RootID: home.ID, Relative: "."}, Cleanup: workspace.CleanupPolicy{Retention: workspace.Keep},
		Effects: []workspace.EffectGrant{{Kind: workspace.DirectoryEffect, RootID: home.ID, AuthorizationID: "fixture-grant", Version: "1"}, {Kind: workspace.DirectoryEffect, RootID: identity.ID, AuthorizationID: "fixture-grant", Version: "1"}, {Kind: workspace.ArtifactEffect, RootID: candidate.ID, AuthorizationID: "fixture-grant", Version: "1"}},
	}
	in := Input{SchemaVersion: SchemaVersion, Definition: Definition{Name: "fixture", Revision: "revision-1", Provenance: origin, Policy: Policy{Profile: profile, Restrictions: sandbox.ResolvedAccessPolicy{Mode: sandbox.ConfinementDisabled}, Provenance: origin}},
		Dispatch: Dispatch{Provenance: origin, Provider: runtimes.Claude, Mode: runtimes.ModeSubprocessPerTurn, Model: "fixture-model", Effort: "high", Prompt: "--dangerously-skip-permissions"}, Workspace: spec,
		Context: ContextHook{Provenance: origin, Artifacts: []ContextArtifact{{Field: plan.Instructions, Requirement: plan.Required, Content: render.Content{Pin: render.Pin{Source: "fixture-definition", Revision: "revision-1"}, Body: []byte("fixture instructions\n")}}}},
	}
	now := time.Now().UTC()
	resources := workspace.Resources{Roots: []workspace.RootRef{home, identity, current, candidate}, Grants: slices.Clone(spec.Effects), LockNamespace: control.Path, LockRoot: control, Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}}
	observed := workspace.Observations{At: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), FenceVersion: "1", Capabilities: slices.Clone(resources.Capabilities)}
	for _, r := range append(slices.Clone(resources.Roots), control) {
		observed.Roots = append(observed.Roots, workspace.RootObservation{RootID: r.ID, DeclaredPath: r.Path, CanonicalPath: r.Path, CanonicalBase: r.AllowedBase, Owner: r.Owner, Exists: r.ID == control.ID, Directory: r.ID == control.ID, Empty: r.ID == control.ID})
	}
	f := &fixture{now: now, observed: observed}
	host := HostInputs{HostInputDTO: HostInputDTO{CredentialAvailability: render.CredentialAvailable, Resources: resources, Observations: observed, Ceiling: permission.Ceiling{Modes: []permission.Mode{permission.ModePlan}}, Provenance: origin, Environment: []EnvironmentEntry{{Delta: provider.EnvDelta{Name: "TEAM_ROLE", Value: "fixture", Operation: provider.EnvSet, Precedence: provider.EnvCallerWins}, Provenance: origin}}}, Ports: workspace.Ports{Clock: f, Host: f, Locks: f, Observations: f, ReceiptStore: f}}
	return in, host, f
}

func TestPlanPureFrozenCompleteArgvAndCanonicalArtifacts(t *testing.T) {
	in, host, f := inputFixture(t)
	in.Dispatch.SystemPrompt = "--fixture-system"
	p, err := Plan(in, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatal("pure plan touched a port", f.calls)
	}
	d := p.Description()
	if d.Executable != "claude" || !slices.Contains(d.Argv, "fixture-model") || !slices.Contains(d.Argv, "--permission-mode") || d.Argv[len(d.Argv)-2] != "--" || d.Argv[len(d.Argv)-1] != in.Dispatch.Prompt || !slices.Contains(d.Argv, "--system-prompt=--fixture-system") {
		t.Fatalf("incomplete or unsafe argv %#v", d.Argv)
	}
	if reflect.DeepEqual(d.Argv, d.Bindings[0].Argv) {
		t.Fatal("table delta was substituted for process argv")
	}
	in.Context.Artifacts[0].Content.Body[0] = 'X'
	host.Resources.Roots[0].Owner = "changed"
	host.Environment[0].Delta.Value = "changed"
	d.Argv[0] = "changed"
	d.Provenance.Hooks = append(d.Provenance.Hooks, Provenance{"changed", "changed"})
	if p.Description().Argv[0] == "changed" || p.Description().Resources.Roots[0].Owner == "changed" || p.Description().Environment[len(p.Description().Environment)-1].Delta.Value == "changed" {
		t.Fatal("caller mutation escaped plan")
	}
	for _, a := range p.workspace.Actions() {
		for _, e := range a.Request.Artifacts.Entries {
			if e.Path == "CLAUDE.md" && string(e.Bytes) != "fixture instructions\n" {
				t.Fatal("canonical body changed", string(e.Bytes))
			}
			if e.Path == "auth.json" {
				t.Fatal("legacy credential placeholder entered managed artifacts")
			}
		}
	}
}

func TestPrepareEarlyRefusalsKeepProvenanceAndTouchNoPorts(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*Input, *HostInputs)
	}{
		{"model", "missing_dispatch_selection", func(i *Input, h *HostInputs) { i.Dispatch.Model = "" }},
		{"effort", "missing_dispatch_selection", func(i *Input, h *HostInputs) { i.Dispatch.Effort = "" }},
		{"runtime", "missing_dispatch_selection", func(i *Input, h *HostInputs) { i.Dispatch.Mode = "" }},
		{"provider", "missing_dispatch_selection", func(i *Input, h *HostInputs) { i.Dispatch.Provider = "" }},
		{"unknown policy", "unknown_permission_profile", func(i *Input, h *HostInputs) { i.Definition.Policy.Profile.Name = "catalog-auto" }},
		{"ceiling", "permission_ceiling", func(i *Input, h *HostInputs) { h.Ceiling.Modes = nil }},
		{"binding version", "policy_binding_mismatch", func(i *Input, h *HostInputs) { i.Definition.Policy.Profile.Version = "foreign" }},
		{"workspace policy mismatch", "policy_binding_mismatch", func(i *Input, h *HostInputs) {
			i.Workspace.Sandbox.Profile = permission.ProfileBinding{Name: "yolo", Mode: permission.ModeYolo}
		}},
		{"native effort mismatch", "effort_mismatch", func(i *Input, h *HostInputs) {
			i.Settings = NativeSettings{Provenance: i.Context.Provenance, Claude: &ClaudeSettings{EffortLevel: "low"}}
		}},
		{"unpinned instructions", "unpinned_context", func(i *Input, h *HostInputs) { i.Context.Artifacts[0].Content.Pin.Source = "" }},
		{"approvals", "unsupported_policy_references", func(i *Input, h *HostInputs) {
			i.Definition.Policy.Approvals = []PolicyReference{{URI: "fixture:approval", Digest: strings.Repeat("a", 64)}}
		}},
		{"escalation", "unsupported_policy_references", func(i *Input, h *HostInputs) {
			i.Definition.Policy.Escalation = []PolicyReference{{URI: "fixture:escalation", Digest: strings.Repeat("b", 64)}}
		}},
		{"option-valued model", "invalid_dispatch_value", func(i *Input, h *HostInputs) { i.Dispatch.Model = "--dangerously-skip-permissions" }},
		{"option-valued resume", "invalid_dispatch_value", func(i *Input, h *HostInputs) { i.Dispatch.ResumeID = "--unsafe-option" }},
		{"required hook", "unsupported_required_hook", func(i *Input, h *HostInputs) {
			i.Hooks = []HookBinding{{Event: "Stop", Command: "fixture-command", Required: true, Provenance: i.Context.Provenance}}
		}},
		{"native policy", "native_policy_mixture", func(i *Input, h *HostInputs) {
			i.Settings = NativeSettings{Provenance: i.Context.Provenance, Claude: &ClaudeSettings{PermissionsDefaultMode: "auto"}}
		}},
		{"cross provider native", "provider_settings_mismatch", func(i *Input, h *HostInputs) {
			i.Dispatch.Provider = runtimes.Codex
			i.Settings = NativeSettings{Provenance: i.Context.Provenance, Claude: &ClaudeSettings{TUI: "fullscreen"}}
		}},
		{"unsupported native", "unsupported_tui", func(i *Input, h *HostInputs) {
			i.Settings = NativeSettings{Provenance: i.Context.Provenance, Claude: &ClaudeSettings{TUI: "invented"}}
		}},
		{"unpinned subagent", "unpinned_context", func(i *Input, h *HostInputs) {
			i.Context.Artifacts = append(i.Context.Artifacts, ContextArtifact{Field: plan.Subagents, Requirement: plan.Required, Components: map[string]string{"name": "reviewer"}, Content: render.Content{Body: []byte("review")}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, host, f := inputFixture(t)
			tc.change(&in, &host)
			result, err := Prepare(context.Background(), in, host)
			var be *Error
			if !errors.As(err, &be) || be.Code != tc.code || result.ArtifactsComplete() {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if result.Description.Provenance.Definition != in.Definition.Provenance || result.Description.Provenance.Host != host.Provenance {
				t.Fatal("refusal lost provenance")
			}
			if len(f.calls) != 0 {
				t.Fatal("refusal touched ports", f.calls)
			}
			if _, err := os.Stat(in.Workspace.Boot.Candidate.Path); !os.IsNotExist(err) {
				t.Fatal("refusal mutated candidate", err)
			}
		})
	}
}

func TestExplicitEffortRefusesUnknownAndPreservesSupportedValues(t *testing.T) {
	for _, tc := range []struct {
		provider    runtimes.ID
		supported   []string
		unsupported []string
	}{
		{runtimes.Claude, []string{"low", "medium", "high", "xhigh"}, []string{"definitely-unsupported", "HIGH", "auto", "max", "ultra"}},
		{runtimes.Codex, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, []string{"definitely-unsupported", "HIGH", "auto", "custom-unattested"}},
		{runtimes.Antigravity, []string{"low", "medium", "high"}, []string{"definitely-unsupported", "HIGH", "auto", "xhigh", "max"}},
	} {
		for _, effort := range tc.unsupported {
			t.Run(string(tc.provider)+"/refuse/"+effort, func(t *testing.T) {
				in, host, f := inputFixture(t)
				in.Dispatch.Provider, in.Dispatch.Effort = tc.provider, effort
				result, err := Prepare(context.Background(), in, host)
				var be *Error
				if !errors.As(err, &be) || be.Phase != PhasePlan || be.Code != "unsupported_effort" {
					t.Fatalf("unknown effort reached preparation: error=%v status=%s calls=%v", err, result.Apply.Status, f.calls)
				}
				if len(f.calls) != 0 || result.ArtifactsComplete() {
					t.Fatal("effort refusal touched ports or earned artifacts", f.calls)
				}
				if result.Description.Effort != effort || result.Description.Provenance.Dispatch != in.Dispatch.Provenance {
					t.Fatal("refused intent was normalized or lost")
				}
			})
		}
		for _, effort := range tc.supported {
			t.Run(string(tc.provider)+"/preserve/"+effort, func(t *testing.T) {
				in, host, f := inputFixture(t)
				in.Dispatch.Provider, in.Dispatch.Effort = tc.provider, effort
				p, err := Plan(in, host)
				if err != nil || !p.Valid() || len(f.calls) != 0 || p.Description().Effort != effort {
					t.Fatal("supported explicit effort failed", err, f.calls)
				}
				if tc.provider == runtimes.Antigravity {
					args := p.Description().Argv
					i := slices.Index(args, "--effort")
					if i < 0 || i+1 >= len(args) || args[i+1] != effort {
						t.Fatal("explicit effort was not serialized", args)
					}
				} else {
					result, err := Prepare(context.Background(), in, host)
					if err != nil || !result.ArtifactsComplete() {
						t.Fatal("supported effort failed concrete artifact preparation", err)
					}
					b, err := os.ReadFile(result.Description.SettingsPath)
					if err != nil || !strings.Contains(string(b), effort) {
						t.Fatal("explicit effort missing from canonical settings", err, string(b))
					}
				}
			})
		}
	}
}

func TestExplicitChildCWDIsHonoredOrRefusedBeforePorts(t *testing.T) {
	for _, provider := range []runtimes.ID{runtimes.Claude, runtimes.Codex, runtimes.Antigravity} {
		for _, matching := range []bool{false, true} {
			t.Run(string(provider)+"/matching="+fmt.Sprint(matching), func(t *testing.T) {
				in, host, f := inputFixture(t)
				in.Dispatch.Provider = provider
				in.Workspace.CWD.ProtocolProject = in.Workspace.Home.Root.Path
				in.Workspace.CWD.Child = in.Workspace.Home.Root.Path
				if matching {
					in.Workspace.CWD.Child = in.Workspace.Boot.Candidate.Path
				}
				if matching {
					p, err := Plan(in, host)
					if err != nil || !p.Valid() || p.Description().CWD != in.Workspace.CWD.Child || len(f.calls) != 0 {
						t.Fatal("matching child refused or canonical boot cwd changed", err, p.Description().CWD)
					}
					return
				}
				result, err := Prepare(context.Background(), in, host)
				var be *Error
				if !errors.As(err, &be) || be.Phase != PhaseProject || be.Code != "unsupported_child_cwd" || len(f.calls) != 0 || result.ArtifactsComplete() {
					t.Fatalf("different child was silently dropped: error=%v cwd=%s calls=%v", err, result.Description.CWD, f.calls)
				}
			})
		}
	}
}

func TestPriorObligationsAndReturnedSlicesRemainDetached(t *testing.T) {
	in, host, _ := inputFixture(t)
	prior := workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: in.Workspace.Home.Root.ID, Code: "fixture-prior-obligation"}
	host.Resources.RecoveryReceipts = []workspace.Receipt{{SchemaVersion: workspace.SchemaVersion, OperationID: "fixture-prior-operation", InputDigest: "fixture-prior-input", IdentityKey: in.Workspace.Identity.EncodedKey, Phase: workspace.Interrupted, Roots: []workspace.RootReceipt{{Root: in.Workspace.Home.Root}}, Obligations: []workspace.Obligation{prior}}}
	got, err := Prepare(context.Background(), in, host)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Obligations(), prior) || !slices.Contains(got.Apply.Receipt.Obligations, prior) || !slices.Contains(got.Apply.Retained, in.Workspace.Home.Root) {
		t.Fatal("prior recovery lost", got.Obligations())
	}
	copy := got.Obligations()
	copy[0].Code = "mutated"
	if slices.ContainsFunc(got.Obligations(), func(o workspace.Obligation) bool { return o.Code == "mutated" }) {
		t.Fatal("obligation accessor leaked earned evidence")
	}
}

func TestTransportDescriptionsAndExplicitNativeDispatch(t *testing.T) {
	for _, tc := range []struct {
		provider runtimes.ID
		mode     runtimes.Mode
		delivery string
	}{{runtimes.Claude, runtimes.ModeStreamingStdio, "stdin-json-line"}, {runtimes.Claude, runtimes.ModePTY, "stdin-text"}, {runtimes.Codex, runtimes.ModeJSONRPCStdio, "rpc-turn"}, {runtimes.Antigravity, runtimes.ModeSubprocessPerTurn, "argv"}} {
		t.Run(string(tc.provider)+"/"+string(tc.mode), func(t *testing.T) {
			in, host, _ := inputFixture(t)
			in.Dispatch.Provider, in.Dispatch.Mode = tc.provider, tc.mode
			p, err := Plan(in, host)
			if err != nil {
				t.Fatal(err)
			}
			d := p.Description()
			if d.Delivery.Kind != tc.delivery || d.Model != in.Dispatch.Model || d.Effort != in.Dispatch.Effort {
				t.Fatal("dispatch or delivery lost", d.Delivery)
			}
			if tc.delivery == "stdin-json-line" {
				var frame struct {
					Type    string
					Message struct{ Role, Content string }
				}
				if err := json.Unmarshal(d.Delivery.Stdin, &frame); err != nil || frame.Type != "user" || frame.Message.Content != in.Dispatch.Prompt {
					t.Fatal("invalid streaming frame", err)
				}
				if slices.Contains(d.Argv, in.Dispatch.Prompt) {
					t.Fatal("streaming prompt became argv")
				}
			}
			if tc.provider == runtimes.Antigravity && !slices.Contains(d.Argv, "--effort") {
				t.Fatal("explicit effort not bound", d.Argv)
			}
		})
	}
}

func TestEmptyPackageBytesAndPinnedSubagentUseCanonicalRenderer(t *testing.T) {
	in, host, _ := inputFixture(t)
	in.Context.Artifacts = append(in.Context.Artifacts, ContextArtifact{Field: plan.Skills, Requirement: plan.Required, Components: map[string]string{"name": "fixture-skill"}, Content: render.Content{Pin: render.Pin{Source: "fixture-skill", Revision: "1"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte{}}}}}}, ContextArtifact{Field: plan.Subagents, Requirement: plan.Required, Components: map[string]string{"name": "reviewer"}, Content: render.Content{Pin: render.Pin{Source: "fixture-reviewer", Revision: "1"}, Body: []byte("fixture reviewer")}})
	p, err := Plan(in, host)
	if err != nil {
		t.Fatal(err)
	}
	empty, review := false, false
	for _, a := range p.workspace.Actions() {
		for _, e := range a.Request.Artifacts.Entries {
			switch e.Path {
			case ".claude/skills/fixture-skill/SKILL.md":
				empty = e.Bytes != nil && len(e.Bytes) == 0
			case ".claude/agents/reviewer.md":
				review = strings.Contains(string(e.Bytes), "fixture reviewer")
			}
		}
	}
	if !empty || !review {
		t.Fatal("finished neutral artifacts lost empty content or registration")
	}
}

func TestBareSkillsAreBoundIntoCompleteLaunch(t *testing.T) {
	in, host, _ := inputFixture(t)
	in.Dispatch.Variant = plan.VariantBare
	in.Context.Artifacts = append(in.Context.Artifacts, ContextArtifact{Field: plan.Skills, Requirement: plan.Required, Components: map[string]string{"name": "fixture-skill"}, Content: render.Content{Pin: render.Pin{Source: "fixture-skill", Revision: "1"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture skill")}}}}})
	p, err := Plan(in, host)
	if err != nil {
		t.Fatal(err)
	}
	args := p.Description().Argv
	bound := false
	for i, arg := range args {
		if arg == "--add-dir" && i+1 < len(args) && args[i+1] == in.Workspace.Boot.Candidate.Path {
			bound = true
		}
	}
	if !bound {
		t.Fatal("required bare skills are present but absent from complete launch", args)
	}
}

func TestMissingLaunchFileAndInvalidEncodingRefuseBeforePorts(t *testing.T) {
	for _, kind := range []string{"missing bare instructions", "definition encoding", "host environment encoding"} {
		t.Run(kind, func(t *testing.T) {
			in, host, f := inputFixture(t)
			switch kind {
			case "missing bare instructions":
				in.Dispatch.Variant = plan.VariantBare
				in.Context.Artifacts = nil
			case "definition encoding":
				in.Definition.Provenance.Source = string([]byte{0xff})
			case "host environment encoding":
				host.Environment[0].Delta.Value = string([]byte{0xff})
			}
			got, err := Prepare(context.Background(), in, host)
			if err == nil || got.ArtifactsComplete() || len(f.calls) > 0 {
				t.Fatal("unresolved file or invalid text was accepted or touched ports", err, f.calls)
			}
		})
	}
}

func TestResolvedNativeSettingsPreserveValuesAndOptionalHooksAreDiagnosed(t *testing.T) {
	in, host, _ := inputFixture(t)
	no, yes := false, true
	in.Settings = NativeSettings{Provenance: in.Context.Provenance, Claude: &ClaudeSettings{TUI: "fullscreen", EffortLevel: "high", AwaySummaryEnabled: &no, SkipDangerousModePermissionPrompt: &yes, SkipAutoPermissionPrompt: &yes, EnabledPlugins: map[string]bool{"fixture-plugin": false}, Environment: map[string]string{"CLAUDE_CODE_DISABLE_BUNDLED_SKILLS": "1"}}}
	in.Hooks = []HookBinding{{Event: "Stop", Command: "fixture-command", Provenance: in.Context.Provenance}}
	p, err := Plan(in, host)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	for _, a := range p.workspace.Actions() {
		for _, e := range a.Request.Artifacts.Entries {
			if e.Path == ".claude/settings.json" {
				if err := json.Unmarshal(e.Bytes, &settings); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if settings["tui"] != "fullscreen" || settings["awaySummaryEnabled"] != false || settings["skipAutoPermissionPrompt"] != true || settings["effortLevel"] != "high" {
		t.Fatal("native intent lost", settings)
	}
	if len(p.Description().Diagnostics) == 0 || p.Description().Diagnostics[0].Code != "optional_hook_omitted" {
		t.Fatal("optional hook was silently lost")
	}
}

func TestPrepareCompleteArtifactsAndPartialDirectoryAndReceiptFailures(t *testing.T) {
	for _, kind := range []string{"success", "directory", "committed receipt"} {
		t.Run(kind, func(t *testing.T) {
			in, host, f := inputFixture(t)
			switch kind {
			case "directory":
				f.failDirectory = in.Workspace.Boot.IdentityRoot.ID
			case "committed receipt":
				f.failPhase = workspace.ArtifactsCommitted
			}
			got, err := Prepare(context.Background(), in, host)
			if kind == "success" {
				if err != nil || !got.ArtifactsComplete() || got.Apply.Status != workspace.Partial || len(got.Obligations()) == 0 {
					t.Fatalf("result=%+v err=%v", got, err)
				}
				body, err := os.ReadFile(filepath.Join(in.Workspace.Boot.Candidate.Path, "CLAUDE.md"))
				if err != nil || string(body) != "fixture instructions\n" {
					t.Fatal("managed canonical body missing", err)
				}
				got.Apply.Receipt.InputDigest = "foreign"
				if got.ArtifactsComplete() {
					t.Fatal("mutated result kept earned completion")
				}
				return
			}
			var be *Error
			if !errors.As(err, &be) || be.Phase != PhaseMaterialize || got.ArtifactsComplete() || got.Apply.Status != workspace.Partial || len(got.Apply.Retained) == 0 || len(got.Obligations()) == 0 {
				t.Fatalf("partial accounting lost %+v err=%v", got, err)
			}
			if _, err := os.Stat(in.Workspace.Home.Root.Path); err != nil {
				t.Fatal("successful earlier directory not retained", err)
			}
			if got.Description.Provenance.Definition != in.Definition.Provenance || got.Apply.Receipt.OperationID != in.Workspace.OperationID {
				t.Fatal("partial result lost original binding")
			}
		})
	}
}

func TestCancellationAndBadProjectionRefuseBeforeMutation(t *testing.T) {
	in, host, f := inputFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := Prepare(ctx, in, host)
	if !errors.Is(err, context.Canceled) || len(f.calls) != 0 || got.Description.Provenance.Context != in.Context.Provenance {
		t.Fatal("cancelled preparation touched ports or lost provenance", err)
	}
	host.Environment = append(host.Environment, EnvironmentEntry{Delta: provider.EnvDelta{Name: "TEAM_ROLE", Value: "collision", Operation: provider.EnvSet, Precedence: provider.EnvCallerWins}, Provenance: host.Provenance})
	got, err = Prepare(context.Background(), in, host)
	var be *Error
	if !errors.As(err, &be) || be.Phase != PhaseProject || len(f.calls) != 0 || got.ArtifactsComplete() {
		t.Fatal("failed process projection reached writer", err)
	}
}

func TestCodexExplicitModelEffortAndResumePosition(t *testing.T) {
	in, host, _ := inputFixture(t)
	in.Dispatch.Provider = runtimes.Codex
	in.Dispatch.ResumeID = "fixture-thread"
	p, err := Plan(in, host)
	if err != nil {
		t.Fatal(err)
	}
	args := p.Description().Argv
	if !slices.Contains(args, `model="fixture-model"`) || !slices.Contains(args, "resume") || args[len(args)-3] != "fixture-thread" || args[len(args)-2] != "--" {
		t.Fatal("Codex resume or model placement changed", args)
	}
	found := false
	for _, a := range p.workspace.Actions() {
		for _, e := range a.Request.Artifacts.Entries {
			if e.Path == "auth.json" {
				t.Fatal("managed credential placeholder")
			}
			if e.Path == "config.toml" {
				found = strings.Contains(string(e.Bytes), `model_reasoning_effort = "high"`)
			}
		}
	}
	if !found {
		t.Fatal("explicit Codex effort missing from native config")
	}
}

func TestRequestDoesNotBecomeGrantAndCallbacksCannotRewriteFrozenInputs(t *testing.T) {
	in, host, f := inputFixture(t)
	host.Resources.Grants = nil
	_, err := Prepare(context.Background(), in, host)
	if err == nil || len(f.calls) != 0 {
		t.Fatal("request authorized its own effect", err)
	}
	in, host, f = inputFixture(t)
	f.validate = func() {
		in.Dispatch.Model = "changed"
		in.Context.Artifacts[0].Content.Body[0] = 'X'
		host.Environment[0].Delta.Value = "changed"
	}
	got, err := Prepare(context.Background(), in, host)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description.Model != "fixture-model" || got.Description.Environment[len(got.Description.Environment)-1].Delta.Value != "fixture" {
		t.Fatal("callback rewrote description")
	}
	body, err := os.ReadFile(filepath.Join(in.Workspace.Boot.Candidate.Path, "CLAUDE.md"))
	if err != nil || string(body) != "fixture instructions\n" {
		t.Fatal("callback rewrote artifacts", err)
	}
}
