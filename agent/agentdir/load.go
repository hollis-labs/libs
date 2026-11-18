package agentdir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hollis-labs/libs/mesh/core/agentdef"
	"gopkg.in/yaml.v3"
)

const maxFileSize = 1 << 20

// ContextFile describes an authored context document.
type ContextFile struct {
	Name, Path string
	Size       int64
	Body       string
}

// Snapshot contains the validated inputs read from an agent directory.
type Snapshot struct {
	Definition      agentdef.Definition
	Profile         Profile
	Directory       string
	MCP             MCPConfig
	MCPRaw          json.RawMessage
	Skills          []SkillRef
	Context         []ContextFile
	PermissionsFile string
	PermissionsRaw  []byte
	Digest          string
}

// Load re-reads the authored inputs and validates their formats.
func Load(dir string) (Snapshot, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := realDir(root, false); err != nil {
		return Snapshot{}, err
	}
	hash := sha256.New()
	read := func(name string, optional bool) ([]byte, error) {
		data, err := readFile(filepath.Join(root, name), optional)
		if err == nil && data != nil {
			fmt.Fprintf(hash, "%d:%s%d:", len(name), name, len(data))
			hash.Write(data)
		}
		return data, err
	}

	agent, err := read("agent.md", false)
	if err != nil {
		return Snapshot{}, err
	}
	def, err := agentdef.Parse(agent)
	if err != nil {
		return Snapshot{}, fmt.Errorf("agent.md: %w", err)
	}
	host, err := read("host.yaml", false)
	if err != nil {
		return Snapshot{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(host))
	dec.KnownFields(true)
	var profile Profile
	if err := dec.Decode(&profile); err != nil {
		return Snapshot{}, fmt.Errorf("%w: host.yaml: %w", ErrProfile, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Snapshot{}, fmt.Errorf("%w: host.yaml must contain one document", ErrProfile)
	}
	if err := profile.Validate(def); err != nil {
		return Snapshot{}, err
	}

	out := Snapshot{Definition: *def, Profile: profile, Directory: root}
	out.MCPRaw, err = read(".mcp.json", true)
	if err != nil {
		return Snapshot{}, err
	}
	if out.MCPRaw != nil {
		out.MCP, err = ParseMCP(out.MCPRaw)
		if err != nil {
			return Snapshot{}, err
		}
	}
	permissions, err := read("permissions.yaml", true)
	if err != nil {
		return Snapshot{}, err
	}
	if permissions != nil {
		out.PermissionsFile = filepath.Join(root, "permissions.yaml")
		out.PermissionsRaw = permissions
	}

	out.Skills, err = listSkills(filepath.Join(root, "skills"), func(path string) ([]byte, error) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		return read(rel, true)
	})
	if err != nil {
		return Snapshot{}, err
	}

	contextDir := filepath.Join(root, "context")
	exists, err := realDir(contextDir, true)
	if err != nil {
		return Snapshot{}, err
	}
	if exists {
		entries, err := os.ReadDir(contextDir)
		if err != nil {
			return Snapshot{}, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if filepath.Ext(name) != ".md" {
				continue
			}
			data, err := read(filepath.Join("context", name), false)
			if err != nil {
				return Snapshot{}, err
			}
			out.Context = append(out.Context, ContextFile{Name: name, Path: filepath.Join(contextDir, name), Size: int64(len(data)), Body: string(data)})
		}
	}
	out.Digest = hex.EncodeToString(hash.Sum(nil))
	return out, nil
}

func realDir(path string, optional bool) (bool, error) {
	st, err := os.Lstat(path)
	if optional && os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.IsDir() {
		return false, fmt.Errorf("agentdir: %s must be a real directory", path)
	}
	return true, nil
}

func readFile(path string, optional bool) ([]byte, error) {
	st, err := os.Lstat(path)
	if optional && os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxFileSize {
		return nil, fmt.Errorf("agentdir: %s must be a regular file at most 1 MiB", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(st, opened) {
		return nil, fmt.Errorf("agentdir: %s changed while opening", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("agentdir: %s exceeds 1 MiB", path)
	}
	return data, nil
}
