// Package agentdir loads an authored agent directory and applies host-side
// lifetime rules around the Tradecraft SDK. Files remain the source of truth;
// stores and runners are injected by the application host.
package agentdir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/libs/mesh/core/agentdef"
	"gopkg.in/yaml.v3"
)

type Kind string

const (
	Stable   Kind = "stable"
	Task     Kind = "task"
	Embedded Kind = "embedded"
)

type Lifetime string

const (
	OnDemand     Lifetime = "on-demand"
	UntilTaskEnd Lifetime = "until-task-end"
	Application  Lifetime = "application"
)

// Profile is host policy, separate from agentdef's portable continuity policy.
// A task ID names one task; an application ID scopes an embedded operative.
type Profile struct {
	Kind          Kind     `yaml:"kind" json:"kind"`
	Lifetime      Lifetime `yaml:"lifetime" json:"lifetime"`
	SeatID        string   `yaml:"seat_id,omitempty" json:"seat_id,omitempty"`
	TaskID        string   `yaml:"task_id,omitempty" json:"task_id,omitempty"`
	ApplicationID string   `yaml:"application_id,omitempty" json:"application_id,omitempty"`
}

var ErrProfile = errors.New("agentdir: invalid host profile")

func (p Profile) Validate(def *agentdef.Definition) error {
	if def == nil {
		return fmt.Errorf("%w: definition required", ErrProfile)
	}
	switch p.Kind {
	case Stable:
		if p.Lifetime != OnDemand || strings.TrimSpace(p.SeatID) == "" || p.TaskID != "" || p.ApplicationID != "" || def.Continuity.Mode != agentdef.Durable {
			return fmt.Errorf("%w: stable requires durable continuity, on-demand lifetime and seat_id only", ErrProfile)
		}
	case Task:
		if p.Lifetime != UntilTaskEnd || strings.TrimSpace(p.TaskID) == "" || p.SeatID != "" || p.ApplicationID != "" || def.Continuity.Mode != agentdef.Durable {
			return fmt.Errorf("%w: task requires durable continuity, until-task-end lifetime and task_id only", ErrProfile)
		}
	case Embedded:
		if p.Lifetime != Application || strings.TrimSpace(p.ApplicationID) == "" || p.SeatID != "" || p.TaskID != "" {
			return fmt.Errorf("%w: embedded requires application lifetime and application_id only", ErrProfile)
		}
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrProfile, p.Kind)
	}
	return nil
}

type Snapshot struct {
	Definition       agentdef.Definition
	Profile          Profile
	Directory        string
	MCP              json.RawMessage // settings are passed to the host, never applied implicitly
	SkillsDirectory  string          // optional; host resolver verifies definition pins before use
	ContextDirectory string          // optional; host resolver verifies definition pins before use
}

// Load re-reads every authored file. Reloading never treats database projections
// as a definition. Path checks keep optional settings inside the directory.
func Load(dir string) (Snapshot, error) {
	var out Snapshot
	root, err := filepath.Abs(dir)
	if err != nil {
		return out, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return out, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return out, fmt.Errorf("agentdir: directory must be a real directory")
	}
	read := func(name string, required bool) ([]byte, error) {
		path := filepath.Join(root, name)
		st, e := os.Lstat(path)
		if os.IsNotExist(e) && !required {
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Size() > 1<<20 {
			return nil, fmt.Errorf("agentdir: %s must be a regular file at most 1 MiB", name)
		}
		return os.ReadFile(path)
	}
	data, err := read("agent.md", true)
	if err != nil {
		return out, err
	}
	def, err := agentdef.Parse(data)
	if err != nil {
		return out, err
	}
	data, err = read("host.yaml", true)
	if err != nil {
		return out, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var profile Profile
	if err = dec.Decode(&profile); err != nil {
		return out, fmt.Errorf("%w: host.yaml: %v", ErrProfile, err)
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return out, fmt.Errorf("%w: host.yaml must contain one document: %v", ErrProfile, err)
	}
	if err = profile.Validate(def); err != nil {
		return out, err
	}
	mcp, err := read(".mcp.json", false)
	if err != nil {
		return out, err
	}
	if len(mcp) > 0 {
		var settings map[string]json.RawMessage
		j := json.NewDecoder(bytes.NewReader(mcp))
		if err = j.Decode(&settings); err != nil || settings == nil {
			return out, fmt.Errorf("agentdir: .mcp.json must be a JSON object: %v", err)
		}
		var trailing any
		if err = j.Decode(&trailing); err != io.EOF {
			return out, fmt.Errorf("agentdir: .mcp.json must contain one object")
		}
	}
	optionalDir := func(name string) (string, error) {
		path := filepath.Join(root, name)
		st, e := os.Lstat(path)
		if os.IsNotExist(e) {
			return "", nil
		}
		if e != nil {
			return "", e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("agentdir: %s must be a real directory", name)
		}
		return path, nil
	}
	skills, err := optionalDir("skills")
	if err != nil {
		return out, err
	}
	contextDir, err := optionalDir("context")
	if err != nil {
		return out, err
	}
	return Snapshot{Definition: *def, Profile: profile, Directory: root, MCP: mcp, SkillsDirectory: skills, ContextDirectory: contextDir}, nil
}
