package agentdef

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse decodes one file strictly, then validates it. Optional unknown
// extensions remain in the result. Consumers must supply WithExtensions to
// accept mandatory extensions. No malformed definition is returned on failure.
func Parse(data []byte, opts ...Option) (*Definition, error) {
	fm, body, err := split(data)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(fm, &node); err != nil {
		return nil, parseError("syntax", err.Error())
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, parseError("shape", "frontmatter must be a mapping")
	}
	if err := checkNodes(&node); err != nil {
		return nil, parseError("syntax", err.Error())
	}
	var d Definition
	dec := yaml.NewDecoder(bytes.NewReader(fm))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, parseError("schema", err.Error())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, parseError("syntax", "exactly one YAML document is required")
	}
	d.Body = body
	if err := d.Validate(opts...); err != nil {
		return nil, err
	}
	return &d, nil
}

func split(data []byte) ([]byte, string, error) {
	// Accept common text line endings, but artifact hashes retain the exact bytes.
	text := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimRight(lines[0], " \t") != "---" {
		return nil, "", parseError("frontmatter", "file must start with ---")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			return []byte(strings.Join(lines[1:i], "\n")), strings.TrimSpace(strings.Join(lines[i+1:], "\n")), nil
		}
	}
	return nil, "", parseError("frontmatter", "missing closing ---")
}

// Refuse aliases, merge keys, custom tags, nonstring keys and duplicate keys at
// every depth, including extension data. Definitions are flat authored files;
// YAML composition and ambiguous object keys cannot bypass strict decoding.
func checkNodes(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return fmt.Errorf("line %d: YAML aliases/anchors are not supported", n.Line)
	}
	if (n.Kind == yaml.MappingNode && n.Tag != "!!map") || (n.Kind == yaml.SequenceNode && n.Tag != "!!seq") {
		return fmt.Errorf("line %d: unsupported collection tag %s", n.Line, n.Tag)
	}
	if n.Kind == yaml.ScalarNode {
		switch n.Tag {
		case "!!str", "!!bool", "!!int", "!!float", "!!null":
		default:
			return fmt.Errorf("line %d: unsupported scalar tag %s", n.Line, n.Tag)
		}
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("line %d: keys must be strings", key.Line)
			}
			if seen[key.Value] {
				return fmt.Errorf("line %d: duplicate key %q", key.Line, key.Value)
			}
			seen[key.Value] = true
		}
	}
	for _, c := range n.Content {
		if err := checkNodes(c); err != nil {
			return err
		}
	}
	return nil
}

func parseError(code, message string) error {
	return &ValidationError{Diagnostics: []Diagnostic{{Code: code, Field: "$", Message: message}}}
}
