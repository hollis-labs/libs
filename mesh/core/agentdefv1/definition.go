package agentdef

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// namePattern is the slug rule shared by definition names, skill names,
// capability names and hook names: lowercase alphanumerics separated by single
// hyphens, no leading, trailing or doubled hyphen.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// IdentityStable is the only legal non-empty value of Definition.Identity.
const IdentityStable = "stable"

// Definition is the parsed v1 agent definition: YAML frontmatter plus the
// markdown body below the closing ---. No provider, model, runtime, launch,
// grant, or run-policy field belongs on this type, ever.
type Definition struct {
	Name        string            `yaml:"name" json:"name"`                             // required; ^[a-z0-9]+(-[a-z0-9]+)*$
	Title       string            `yaml:"title,omitempty" json:"title,omitempty"`       // optional; display name
	Description string            `yaml:"description" json:"description"`               // required; non-empty
	Identity    string            `yaml:"identity,omitempty" json:"identity,omitempty"` // "" | "stable" — no other value
	Skills      []string          `yaml:"skills,omitempty" json:"skills,omitempty"`     // Agent Skills names, order preserved
	Tools       []string          `yaml:"tools,omitempty" json:"tools,omitempty"`       // a REQUEST — never validated against a catalog
	Requires    []string          `yaml:"requires,omitempty" json:"requires,omitempty"` // hard; capability names
	Uses        []string          `yaml:"uses,omitempty" json:"uses,omitempty"`         // soft; capability names
	Hooks       []string          `yaml:"hooks,omitempty" json:"hooks,omitempty"`       // NAMES ONLY — no registry, ever
	Icon        string            `yaml:"icon,omitempty" json:"icon,omitempty"`
	Avatar      string            `yaml:"avatar,omitempty" json:"avatar,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Metadata    map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`

	Body string `yaml:"-" json:"body"` // markdown after the closing ---, TrimSpace'd

	// Set by the loader, never parsed from the file. Excluded from Canonical
	// and Digest.
	SourceRef string `yaml:"-" json:"-"` // file path within its layer root
	Layer     string `yaml:"-" json:"-"` // which layer this came from (LoadLayers only)
}

// FieldError is one problem with one frontmatter field.
type FieldError struct {
	Field   string
	Message string
}

// Error implements error as "<field>: <message>".
func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// ValidationError is the error returned by Parse (missing required fields) and
// Validate: every field problem found, in field order.
type ValidationError struct {
	Errors []FieldError
}

// Error joins the field errors with "; ".
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Error()
	}
	return "agentdef: invalid definition: " + strings.Join(parts, "; ")
}

var (
	_ error = (*ValidationError)(nil)
	_ error = FieldError{}
)

// Parse decodes one definition file's bytes. It is strict: an unknown
// top-level field is an error (KnownFields(true) on the yaml.v3 decoder), and
// name and description must be present. Parse checks presence only; Validate
// applies the pattern and enum rules. It does not touch a filesystem.
func Parse(data []byte) (*Definition, error) {
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}

	var d Definition
	dec := yaml.NewDecoder(bytes.NewReader(fm))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil && !errors.Is(err, io.EOF) {
		if ve := unknownFieldErrors(err); ve != nil {
			return nil, ve
		}
		return nil, fmt.Errorf("agentdef: invalid frontmatter: %w", err)
	}
	d.Body = strings.TrimSpace(string(body))

	var ve ValidationError
	if d.Name == "" {
		ve.Errors = append(ve.Errors, FieldError{"name", "is required"})
	}
	if strings.TrimSpace(d.Description) == "" {
		ve.Errors = append(ve.Errors, FieldError{"description", "is required"})
	}
	if len(ve.Errors) > 0 {
		return nil, &ve
	}
	return &d, nil
}

var unknownFieldRe = regexp.MustCompile(`^line (\d+): field (\S+) not found in type `)

// unknownFieldErrors turns yaml.v3's KnownFields complaints into one
// FieldError per unknown key, in file order. It returns nil unless every
// complaint is an unknown-field one.
func unknownFieldErrors(err error) *ValidationError {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return nil
	}
	var ve ValidationError
	for _, msg := range te.Errors {
		m := unknownFieldRe.FindStringSubmatch(msg)
		if m == nil {
			return nil
		}
		ve.Errors = append(ve.Errors, FieldError{m[2], "unknown field (line " + m[1] + ")"})
	}
	return &ve
}

// ParseFile reads and parses one file from fsys, setting SourceRef to path.
func ParseFile(fsys fs.FS, path string) (*Definition, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("agentdef: read %s: %w", path, err)
	}
	d, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("agentdef: parse %s: %w", path, err)
	}
	d.SourceRef = path
	return d, nil
}

// splitFrontmatter splits data into YAML frontmatter and markdown body. The
// file must start with a --- line; the closing --- must be a line of its own.
func splitFrontmatter(data []byte) (frontmatter, body []byte, err error) {
	data = bytes.TrimLeft(data, "\n\r")

	first, rest, ok := bytes.Cut(data, []byte("\n"))
	if !isDelimLine(first) {
		return nil, nil, errors.New("agentdef: file does not start with --- frontmatter delimiter")
	}
	if !ok {
		return nil, nil, errors.New("agentdef: no content after opening --- delimiter")
	}

	offset := 0
	for offset <= len(rest) {
		line, _, more := bytes.Cut(rest[offset:], []byte("\n"))
		if isDelimLine(line) {
			frontmatter = rest[:offset]
			if more {
				body = rest[offset+len(line)+1:]
			}
			return frontmatter, body, nil
		}
		if !more {
			break
		}
		offset += len(line) + 1
	}
	return nil, nil, errors.New("agentdef: missing closing --- delimiter")
}

func isDelimLine(line []byte) bool {
	return string(bytes.TrimRight(line, " \t\r")) == "---"
}
