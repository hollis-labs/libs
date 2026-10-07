package manifest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	identifier = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*$`)
	keyName    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]*$`)
	envName    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	semver     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
)

// ValidID reports whether id is safe as a plugin identity and directory name
// across hosts. Dotted IDs are allowed; empty segments and traversal are not.
func ValidID(id string) bool { return identifier.MatchString(id) }

// ValidVersion accepts SemVer 2.0.0, without a leading v.
func ValidVersion(version string) bool {
	if !semver.MatchString(version) {
		return false
	}
	_, pre, ok := strings.Cut(strings.SplitN(version, "+", 2)[0], "-")
	if !ok {
		return true
	}
	for _, s := range strings.Split(pre, ".") {
		if len(s) > 1 && s[0] == '0' && strings.Trim(s, "0123456789") == "" {
			return false
		}
	}
	return true
}

// Validate checks common structure only, without starting a process or touching
// files. Host extensions, JSON Schema semantics and capability/effect names
// require host validation. All failures are reported in deterministic order.
func (m Manifest) Validate() error {
	var problems []string
	add := func(ok bool, message string) {
		if !ok {
			problems = append(problems, message)
		}
	}
	add(m.SchemaVersion == SchemaVersion, fmt.Sprintf("schema_version must be %d", SchemaVersion))
	add(ValidID(m.ID), "id must contain lowercase alphanumeric segments separated by dots or dashes")
	add(strings.TrimSpace(m.Name) != "", "name is required")
	add(ValidVersion(m.Version), "version must be SemVer without a leading v")
	add(m.Protocol == RequiredProtocol, fmt.Sprintf("protocol must be %d", RequiredProtocol))
	add(m.Runtime == Runtime, `runtime must be "subprocess"`)
	if err := m.validateV2(); err != nil {
		problems = append(problems, err.Error())
	}
	for _, u := range []struct{ name, value string }{{"homepage", m.Homepage}, {"repository", m.Repository}} {
		if u.value == "" {
			continue
		}
		parsed, err := url.Parse(u.value)
		add(err == nil && parsed != nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil,
			u.name+" must be an HTTP(S) URL without credentials")
	}
	add(len(m.Hosts) > 0, "hosts must declare at least one host contract range")
	for _, host := range sortedKeys(m.Hosts) {
		r := m.Hosts[host]
		add(ValidID(host), "invalid host name "+host)
		add(r.Min != "" || r.Max != "", "hosts."+host+" must declare min or max")
		add(r.Min == "" || ValidVersion(r.Min), "hosts."+host+".min must be SemVer")
		add(r.Max == "" || ValidVersion(r.Max), "hosts."+host+".max must be SemVer")
		if err := r.Validate(); err != nil {
			problems = append(problems, "hosts."+host+": "+err.Error())
		}
	}
	for _, ext := range []struct {
		name string
		raw  json.RawMessage
	}{{"cerberus", m.Cerberus}, {"tangent", m.Tangent}, {"nanite", m.Nanite}} {
		if len(ext.raw) == 0 {
			continue
		}
		add(object(ext.raw) && checkJSON(ext.raw) == nil, ext.name+" must be an object with unique keys")
		_, declared := m.Hosts[ext.name]
		add(declared, ext.name+" extension requires a hosts."+ext.name+" range")
	}
	seenCaps := map[string]bool{}
	for i, c := range m.Capabilities {
		prefix := fmt.Sprintf("capabilities[%d]", i)
		add(strings.TrimSpace(c.Name) != "", prefix+".name is required")
		add(!seenCaps[c.Name], prefix+" duplicates capability "+c.Name)
		seenCaps[c.Name] = true
		add(len(c.Metadata) == 0 || checkJSON(c.Metadata) == nil, prefix+".metadata must be valid JSON")
	}
	for _, name := range sortedKeys(m.Config.Fields) {
		f := m.Config.Fields[name]
		prefix := "config.fields." + name
		add(keyName.MatchString(name), prefix+" has an invalid key")
		_, secret := m.Config.Secrets[name]
		add(!secret, prefix+" is also declared as a secret")
		add(f.Env == "" || envName.MatchString(f.Env), prefix+".env must be an environment variable name")
		switch f.Type {
		case "string":
		case "boolean":
			add(f.Default == "" || f.Default == "true" || f.Default == "false", prefix+".default must be true or false")
		case "integer":
			_, err := strconv.ParseInt(f.Default, 10, 64)
			add(f.Default == "" || err == nil, prefix+".default must be an integer")
		case "number":
			add(f.Default == "" || (json.Valid([]byte(f.Default)) && isNumber(f.Default)), prefix+".default must be a JSON number")
		case "select":
			add(len(f.Options) > 0, prefix+".options is required for select")
			add(f.Default == "" || slices.Contains(f.Options, f.Default), prefix+".default must be one of options")
		default:
			add(false, prefix+".type must be string, boolean, integer, number or select")
		}
		add(f.Type == "select" || len(f.Options) == 0, prefix+".options requires type select")
		seen := map[string]bool{}
		for _, option := range f.Options {
			add(option != "" && !seen[option], prefix+".options must be nonempty and unique")
			seen[option] = true
		}
	}
	for _, name := range sortedKeys(m.Config.Secrets) {
		s := m.Config.Secrets[name]
		add(keyName.MatchString(name), "config.secrets."+name+" has an invalid key")
		add(s.Env == "" || envName.MatchString(s.Env), "config.secrets."+name+".env must be an environment variable name")
	}
	seenTools := map[string]bool{}
	for i, tool := range m.Tools {
		prefix := fmt.Sprintf("tools[%d]", i)
		add(keyName.MatchString(tool.Name), prefix+".name is invalid")
		add(!seenTools[tool.Name], prefix+" duplicates tool "+tool.Name)
		seenTools[tool.Name] = true
		add(strings.TrimSpace(tool.Description) != "", prefix+".description is required")
		add(strings.TrimSpace(tool.Effect) != "", prefix+".effect is required (host-defined vocabulary)")
		if a := tool.Annotations; a != nil {
			add(!(a.ReadOnlyHint != nil && *a.ReadOnlyHint && a.DestructiveHint != nil && *a.DestructiveHint),
				prefix+" ("+tool.Name+").annotations.readOnlyHint=true cannot accompany destructiveHint=true")
		}
		var schema map[string]json.RawMessage
		var schemaType string
		if !object(tool.InputSchema) || checkJSON(tool.InputSchema) != nil || json.Unmarshal(tool.InputSchema, &schema) != nil || json.Unmarshal(schema["type"], &schemaType) != nil || schemaType != "object" {
			add(false, prefix+".input_schema must be an inline JSON Schema with type object")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("manifest %q: %s", m.ID, strings.Join(problems, "; "))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func object(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func isNumber(s string) bool {
	var v any
	return json.Unmarshal([]byte(s), &v) == nil && v != nil && strings.ContainsRune("-0123456789", rune(s[0]))
}
