package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/boot"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// These are synthetic resolved observations in private fixtures, not native
// authority. The CLI receives data only and cannot acquire a port or write roots.
func resolvedFixture(t *testing.T) resolvedRequest {
	t.Helper()
	base := t.TempDir()
	key, err := bootkey.Encode("urn:fixture:agent:cli")
	if err != nil {
		t.Fatal(err)
	}
	root := func(id, path string) workspace.RootRef {
		return workspace.RootRef{ID: id, Path: filepath.Join(base, path), AllowedBase: base, Owner: "fixture-owner", Provenance: "fixture"}
	}
	home, identity := root("home", "home"), root("identity", key)
	candidate, current := root("candidate", filepath.Join(key, "candidate")), root("current", filepath.Join(key, "current"))
	control := root("control", "control")
	digest := artifact.DigestBytes([]byte("fixture definition"))
	profile, err := permission.BindProfile("plan", permission.Ceiling{Modes: []permission.Mode{permission.ModePlan}})
	if err != nil {
		t.Fatal(err)
	}
	origin := boot.Provenance{Source: "fixture-resolver", Revision: "revision-1"}
	spec := workspace.Spec{SchemaVersion: workspace.SchemaVersion, OperationID: "fixture-cli-prepare", Operation: workspace.Prepare,
		Identity: workspace.IdentitySpec{AgentURN: "urn:fixture:agent:cli", EncodedKey: key, Session: "session", DefinitionRevision: "revision-1", SemanticDigest: digest, ArtifactDigest: digest, DependencyDigest: digest, Fence: workspace.ResourceRef{ID: "fence", Revision: "1"}},
		Home:     workspace.HomeSpec{Root: home, Layout: workspace.FullHome, Continuity: workspace.Durable, Retention: workspace.Keep},
		Boot:     workspace.BootSpec{IdentityRoot: identity, Current: current, Candidate: candidate, Retention: workspace.RetainForRecovery},
		CWD:      workspace.CWDSpec{RootID: home.ID, Relative: "."}, Cleanup: workspace.CleanupPolicy{Retention: workspace.Keep},
		Effects: []workspace.EffectGrant{{Kind: workspace.DirectoryEffect, RootID: home.ID, AuthorizationID: "fixture-grant", Version: "1"}, {Kind: workspace.DirectoryEffect, RootID: identity.ID, AuthorizationID: "fixture-grant", Version: "1"}, {Kind: workspace.ArtifactEffect, RootID: candidate.ID, AuthorizationID: "fixture-grant", Version: "1"}},
	}
	input := boot.Input{SchemaVersion: boot.SchemaVersion, Definition: boot.Definition{Name: "fixture", Revision: "revision-1", Provenance: origin, Policy: boot.Policy{Profile: profile, Restrictions: sandbox.ResolvedAccessPolicy{Mode: sandbox.ConfinementDisabled}, Provenance: origin}},
		Dispatch: boot.Dispatch{Provider: runtimes.Claude, Mode: runtimes.ModeSubprocessPerTurn, Model: "fixture-model", Effort: "high", Prompt: "fixture prompt", Provenance: origin}, Workspace: spec,
		Context: boot.ContextHook{Provenance: origin, Artifacts: []boot.ContextArtifact{{Field: plan.Instructions, Requirement: plan.Required, Content: render.Content{Pin: render.Pin{Source: "fixture-definition", Revision: "revision-1"}, Body: []byte("fixture instructions\n")}}}},
	}
	now := time.Now().UTC()
	resources := workspace.Resources{Roots: []workspace.RootRef{home, identity, current, candidate}, Grants: slices.Clone(spec.Effects), LockNamespace: control.Path, LockRoot: control, Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}}
	observed := workspace.Observations{At: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), FenceVersion: "1", Capabilities: slices.Clone(resources.Capabilities)}
	for _, r := range append(slices.Clone(resources.Roots), control) {
		observed.Roots = append(observed.Roots, workspace.RootObservation{RootID: r.ID, DeclaredPath: r.Path, CanonicalPath: r.Path, CanonicalBase: r.AllowedBase, Owner: r.Owner, Exists: r.ID == control.ID, Directory: r.ID == control.ID, Empty: r.ID == control.ID})
	}
	return resolvedRequest{SchemaVersion: boot.SchemaVersion, Input: input, Host: boot.HostInputDTO{CredentialAvailability: render.CredentialAvailable, Resources: resources, Observations: observed, Ceiling: permission.Ceiling{Modes: []permission.Mode{permission.ModePlan}}, Provenance: origin}}
}

func requestBytes(t *testing.T, request resolvedRequest) []byte {
	t.Helper()
	b, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func invoke(t *testing.T, args []string, input []byte) (int, response) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), args, bytes.NewReader(input), &stdout, &stderr)
	var out response
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal("output is not a complete JSON response", err, stdout.String(), stderr.String())
	}
	return exit, out
}

func TestResolvedPlanAndPrepareRefusalPreserveAccountingWithoutMutation(t *testing.T) {
	request := resolvedFixture(t)
	data := requestBytes(t, request)
	for _, planOnly := range []bool{true, false} {
		args := []string{"boot", "--resolved", "-"}
		if planOnly {
			args = append(args, "--plan")
		}
		exit, out := invoke(t, args, data)
		if out.SchemaVersion != boot.SchemaVersion || out.ArtifactsComplete || out.Result.ArtifactsComplete() {
			t.Fatal("CLI minted artifact completion", out)
		}
		d := out.Result.Description
		if d.BootDir != request.Input.Workspace.Boot.Candidate.Path || d.CWD != d.BootDir || d.Executable != "claude" || !slices.Contains(d.Argv, request.Input.Dispatch.Model) || d.Provenance.Definition != request.Input.Definition.Provenance {
			t.Fatal("description is incomplete", d)
		}
		if planOnly {
			if exit != 0 || out.Error != nil || out.Result.Apply.Receipt.OperationID != "" {
				t.Fatal("pure plan claimed application", exit, out)
			}
		} else {
			if exit != 1 || out.Error == nil || out.Error.Phase != boot.PhaseMaterialize || out.Error.Code != workspace.CodeMissingApplyPort {
				t.Fatal("data manufactured host authority", exit, out)
			}
			receipt := out.Result.Apply.Receipt
			if receipt.OperationID != request.Input.Workspace.OperationID || receipt.InputDigest == "" || receipt.IdentityKey != request.Input.Workspace.Identity.EncodedKey || len(receipt.Roots) == 0 || out.Result.Apply.Status != workspace.Partial {
				t.Fatal("missing-port refusal lost engine accounting", out.Result.Apply)
			}
		}
		for _, root := range request.Host.Resources.Roots {
			if _, err := os.Stat(root.Path); !os.IsNotExist(err) {
				t.Fatal("CLI touched a requested root", root.Path, err)
			}
		}
	}
}

func TestResolvedFileAndUnsupportedPolicyKeepDescription(t *testing.T) {
	request := resolvedFixture(t)
	path := filepath.Join(t.TempDir(), "resolved.json")
	if err := os.WriteFile(path, requestBytes(t, request), 0600); err != nil {
		t.Fatal(err)
	}
	exit, out := invoke(t, []string{"boot", "--resolved", path, "--plan"}, nil)
	if exit != 0 || out.Error != nil {
		t.Fatal("explicit file failed", exit, out.Error)
	}
	request.Input.Hooks = []boot.HookBinding{{Event: "Stop", Command: "fixture-command", Required: true, Provenance: request.Input.Context.Provenance}}
	exit, out = invoke(t, []string{"boot", "--resolved", "-"}, requestBytes(t, request))
	if exit != 1 || out.Error == nil || out.Error.Code != "unsupported_required_hook" || out.Result.Description.RequestedHooks[0].Command != "fixture-command" || out.Result.Description.Provenance.Host != request.Host.Provenance {
		t.Fatal("required intent disappeared", exit, out)
	}
}

func TestCLIArgumentsAndDecodeRefuseAmbiguity(t *testing.T) {
	for _, args := range [][]string{nil, {"plant"}, {"boot"}, {"boot", "--json", "-"}, {"boot", "--resolved", "-", "extra"}} {
		exit, out := invoke(t, args, []byte(`{}`))
		if exit != 2 || out.Error == nil {
			t.Fatal("invalid invocation accepted", args, exit, out)
		}
	}
	request := resolvedFixture(t)
	valid := requestBytes(t, request)
	for _, data := range [][]byte{
		[]byte(`{`), []byte(`null`), append(slices.Clone(valid), []byte(` {}`)...),
		bytes.Replace(valid, []byte(`"schema_version":`), []byte(`"schema_version":"unknown.v1","schema_version":`), 1),
		bytes.Replace(valid, []byte(`"effort":"high"`), []byte(`"effort":"unresolved","effort":"high"`), 1),
		bytes.Replace(valid, []byte(`"host":{`), []byte(`"host":{"Ports":{},`), 1),
		bytes.Replace(valid, []byte(boot.SchemaVersion), []byte("unknown.v1"), 1),
		bytes.Replace(valid, []byte(`fixture prompt`), []byte{'f', 0xff}, 1),
		bytes.Replace(valid, []byte(`fixture prompt`), []byte(`\ud800`), 1),
		bytes.Replace(valid, []byte(`fixture prompt`), []byte(`\udc00`), 1),
	} {
		exit, out := invoke(t, []string{"boot", "--resolved", "-", "--plan"}, data)
		if exit != 2 || out.Error == nil || out.ArtifactsComplete {
			t.Fatal("malformed/authority input accepted", exit, out)
		}
	}
	data := bytes.Replace(valid, []byte(`fixture prompt`), []byte(`\ud83d\ude00`), 1)
	exit, out := invoke(t, []string{"boot", "--resolved", "-", "--plan"}, data)
	if exit != 0 || out.Result.Description.Delivery.Prompt != "😀" {
		t.Fatal("valid Unicode pair changed", exit, out)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("fixture read error") }

func TestCLIReadAndOutputErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"boot", "--resolved", "-"}, brokenReader{}, &stdout, &stderr)
	if exit != 2 || !strings.Contains(stdout.String(), "fixture read error") {
		t.Fatal("read error dropped", exit, stdout.String())
	}
	stdout.Reset()
	exit = run(context.Background(), []string{"boot", "--resolved", "-"}, strings.NewReader(strings.Repeat(" ", 32<<20+1)), &stdout, &stderr)
	if exit != 2 || !strings.Contains(stdout.String(), "exceeds 32 MiB") {
		t.Fatal("oversized resolved input accepted", exit, stdout.String())
	}
	stdout.Reset()
	exit = run(context.Background(), []string{"boot", "--resolved", "-", "--plan"}, bytes.NewReader(requestBytes(t, resolvedFixture(t))), brokenWriter{}, &stderr)
	if exit != 1 || !strings.Contains(stderr.String(), "encode result") {
		t.Fatal("output failure dropped", exit, stderr.String())
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("fixture output error") }
