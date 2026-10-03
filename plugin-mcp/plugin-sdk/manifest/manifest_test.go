package manifest_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func example() manifest.Manifest {
	return manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		ID:            "hollis.bookmarks", Name: "Bookmarks", Version: "1.2.3-rc.1+build.7",
		Description: "Save and retrieve links", License: "MIT",
		Homepage:   "https://hollislabs.com/plugins/bookmarks",
		Repository: "https://github.com/hollis-labs/nanite-plugins",
		Protocol:   manifest.RequiredProtocol, Runtime: manifest.Runtime,
		Server:       manifest.Server{Runtime: "binary", Entry: "bin/bookmarks", Engines: map[string]manifest.HostRange{"binary": {Min: "1.0.0"}}},
		Artifact:     testArtifact("bin/bookmarks", true),
		Hosts:        map[string]manifest.HostRange{"nanite": {Min: "0.1.0"}, "tangent": {Min: "1.0.0", Max: "2.0.0"}},
		Capabilities: []subprocess.CapabilityRequest{{Name: "host.query", Reason: "Read session labels", Optional: true, Metadata: json.RawMessage(`{"scope":"sessions"}`)}},
		Config: manifest.Config{
			Fields:  map[string]manifest.Field{"limit": {Type: "integer", Default: "10", Env: "BOOKMARK_LIMIT"}},
			Secrets: map[string]manifest.Secret{"api_key": {Required: true, Env: "BOOKMARK_API_KEY"}},
		},
		Tools:   []manifest.Tool{{Name: "bookmarks_list", Description: "List bookmarks", Effect: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}}}`)}},
		Nanite:  json.RawMessage(`{"ui":{"bundle":"ui/index.js"}}`),
		Tangent: json.RawMessage(`{"routes":[{"method":"GET","path":"/bookmarks"}]}`),
	}
}

func TestGeneratedManifestRoundtrip(t *testing.T) {
	m := example()
	var out bytes.Buffer
	if err := manifest.Encode(&out, m); err != nil {
		t.Fatal(err)
	}
	got, err := manifest.Decode(&out)
	if err != nil {
		t.Fatal(err)
	}
	// Raw extension/schema values retain semantics, including integers, across
	// the generated declaration a separately built host consumes.
	wantJSON, _ := json.Marshal(m)
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("roundtrip mismatch:\n%s\n%s", wantJSON, gotJSON)
	}
	var second bytes.Buffer
	if err := manifest.Encode(&second, got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(second.String(), "\n") {
		t.Fatal("missing newline")
	}
}

func TestValidateRefusesUnsafeOrIncompleteDeclarations(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*manifest.Manifest)
		message string
	}{
		{"legacy schema", func(m *manifest.Manifest) { m.SchemaVersion = 1 }, "schema_version"},
		{"future schema", func(m *manifest.Manifest) { m.SchemaVersion = 99 }, "schema_version"},
		{"builtin", func(m *manifest.Manifest) { m.Runtime = "builtin" }, "runtime"},
		{"wire mismatch", func(m *manifest.Manifest) { m.Protocol = 99 }, "protocol"},
		{"traversal id", func(m *manifest.Manifest) { m.ID = "../bookmarks" }, "id must"},
		{"empty segment", func(m *manifest.Manifest) { m.ID = "hollis..bookmarks" }, "id must"},
		{"name", func(m *manifest.Manifest) { m.Name = " " }, "name is required"},
		{"version", func(m *manifest.Manifest) { m.Version = "v1.2.3" }, "version must"},
		{"URL credentials", func(m *manifest.Manifest) { m.Repository = "https://token@github.com/repo" }, "repository must"},
		{"host missing", func(m *manifest.Manifest) { m.Hosts = nil }, "hosts must"},
		{"host range missing", func(m *manifest.Manifest) { m.Hosts["nanite"] = manifest.HostRange{} }, "min or max"},
		{"host bound invalid", func(m *manifest.Manifest) { m.Hosts["nanite"] = manifest.HostRange{Min: "next"} }, "min must"},
		{"extension host absent", func(m *manifest.Manifest) { delete(m.Hosts, "nanite") }, "requires a hosts.nanite"},
		{"extension array", func(m *manifest.Manifest) { m.Nanite = json.RawMessage(`[]`) }, "must be an object"},
		{"extension null", func(m *manifest.Manifest) { m.Nanite = json.RawMessage(`null`) }, "must be an object"},
		{"extension ambiguity", func(m *manifest.Manifest) { m.Nanite = json.RawMessage(`{"ui":{},"ui":{"bundle":"other.js"}}`) }, "unique keys"},
		{"capability missing", func(m *manifest.Manifest) { m.Capabilities[0].Name = " " }, ".name is required"},
		{"capability duplicate", func(m *manifest.Manifest) { m.Capabilities = append(m.Capabilities, m.Capabilities[0]) }, "duplicates capability"},
		{"invalid metadata", func(m *manifest.Manifest) { m.Capabilities[0].Metadata = json.RawMessage(`{`) }, "metadata"},
		{"secret collision", func(m *manifest.Manifest) { m.Config.Secrets["limit"] = manifest.Secret{} }, "also declared as a secret"},
		{"secret env assignment", func(m *manifest.Manifest) { m.Config.Secrets["api_key"] = manifest.Secret{Env: "KEY=value"} }, "environment variable name"},
		{"config key", func(m *manifest.Manifest) { m.Config.Fields["../x"] = manifest.Field{Type: "string"} }, "invalid key"},
		{"config type", func(m *manifest.Manifest) { m.Config.Fields["limit"] = manifest.Field{Type: "unknown"} }, ".type must"},
		{"invalid integer default", func(m *manifest.Manifest) { m.Config.Fields["limit"] = manifest.Field{Type: "integer", Default: "1.5"} }, "default must be an integer"},
		{"invalid number default", func(m *manifest.Manifest) { m.Config.Fields["limit"] = manifest.Field{Type: "number", Default: "NaN"} }, "default must be a JSON number"},
		{"invalid bool default", func(m *manifest.Manifest) { m.Config.Fields["limit"] = manifest.Field{Type: "boolean", Default: "1"} }, "default must be true or false"},
		{"select empty", func(m *manifest.Manifest) { m.Config.Fields["limit"] = manifest.Field{Type: "select"} }, "options is required"},
		{"select default", func(m *manifest.Manifest) {
			m.Config.Fields["limit"] = manifest.Field{Type: "select", Options: []string{"a"}, Default: "b"}
		}, "default must be one of"},
		{"select duplicate", func(m *manifest.Manifest) {
			m.Config.Fields["limit"] = manifest.Field{Type: "select", Options: []string{"a", "a"}}
		}, "options must be nonempty and unique"},
		{"tool duplicate", func(m *manifest.Manifest) { m.Tools = append(m.Tools, m.Tools[0]) }, "duplicates tool"},
		{"tool name", func(m *manifest.Manifest) { m.Tools[0].Name = "two words" }, ".name is invalid"},
		{"tool description", func(m *manifest.Manifest) { m.Tools[0].Description = "" }, ".description is required"},
		{"effect omitted", func(m *manifest.Manifest) { m.Tools[0].Effect = "" }, ".effect is required"},
		{"string schema", func(m *manifest.Manifest) { m.Tools[0].InputSchema = json.RawMessage(`"{\"type\":\"object\"}"`) }, "inline JSON Schema"},
		{"nonobject schema", func(m *manifest.Manifest) { m.Tools[0].InputSchema = json.RawMessage(`{"type":"array"}`) }, "inline JSON Schema"},
		{"ambiguous schema", func(m *manifest.Manifest) {
			m.Tools[0].InputSchema = json.RawMessage(`{"type":"array","type":"object"}`)
		}, "inline JSON Schema"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := example()
			tc.change(&m)
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("got %v, want %s", err, tc.message)
			}
			var out bytes.Buffer
			if err := manifest.Encode(&out, m); err == nil || out.Len() != 0 {
				t.Fatalf("invalid manifest wrote bytes: %v", err)
			}
		})
	}
}

func TestSemver(t *testing.T) {
	for _, version := range []string{"0.0.0", "1.2.3", "1.2.3-0", "1.2.3-rc.1", "1.2.3+01", "1.2.3-beta+meta-01"} {
		if !manifest.ValidVersion(version) {
			t.Fatalf("refused %s", version)
		}
	}
	for _, version := range []string{"", "v1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3-rc.01", "1.2.3-", "1.2.3+", "1.2.3-rc..1"} {
		if manifest.ValidVersion(version) {
			t.Fatalf("accepted %s", version)
		}
	}
}

func TestDecodeRefusesAmbiguousAndLegacyInput(t *testing.T) {
	var out bytes.Buffer
	if err := manifest.Encode(&out, example()); err != nil {
		t.Fatal(err)
	}
	valid := out.String()
	cases := map[string]string{
		"legacy yaml":             "id: hello\nentrypoint: bin/plugin\n",
		"legacy signature":        strings.Replace(valid, `"runtime": "subprocess"`, `"release":{"signature_url":"https://example.org/signature"},"runtime": "subprocess"`, 1),
		"unknown nested field":    strings.Replace(valid, `"entry": "bin/bookmarks"`, `"shell":true,"entry": "bin/bookmarks"`, 1),
		"secret value":            strings.Replace(valid, `"api_key": {`, `"api_key": {"value":"do-not-store",`, 1),
		"secret default":          strings.Replace(valid, `"api_key": {`, `"api_key": {"default":"do-not-store",`, 1),
		"case alias":              strings.Replace(valid, `"runtime": "subprocess"`, `"Runtime":"builtin","runtime": "subprocess"`, 1),
		"duplicate key":           strings.Replace(valid, `"runtime": "subprocess"`, `"runtime":"builtin","runtime": "subprocess"`, 1),
		"duplicate escaped key":   strings.Replace(valid, `"runtime": "subprocess"`, `"runt\u0069me":"builtin","runtime": "subprocess"`, 1),
		"duplicate extension key": strings.Replace(valid, `"bundle": "ui/index.js"`, `"bundle":"other.js","bundle": "ui/index.js"`, 1),
		"trailing object":         valid + `{}`,
		"trailing junk":           valid + `not-json`,
		"null":                    "null",
		"oversize":                strings.Repeat(" ", manifest.MaxBytes+1),
		"deep JSON":               strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := manifest.Decode(strings.NewReader(raw))
			if err == nil {
				t.Fatal("accepted unsafe input")
			}
			if !reflect.DeepEqual(got, manifest.Manifest{}) {
				t.Fatal("returned partial declaration on failure")
			}
		})
	}
}

func TestHostOwnsOpenVocabulary(t *testing.T) {
	m := example()
	m.Tools[0].Effect = "custom_host.effect"
	m.Capabilities[0].Name = "custom_host.capability"
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestEncodeShortWrite(t *testing.T) {
	if err := manifest.Encode(shortWriter{}, example()); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("got %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	var out bytes.Buffer
	if err := manifest.Encode(&out, example()); err != nil {
		f.Fatal(err)
	}
	f.Add(out.Bytes())
	f.Add([]byte(`{"schema_version":2,"id":"../escape"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := manifest.Decode(bytes.NewReader(raw))
		if err != nil {
			return
		}
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
		var encoded bytes.Buffer
		if err := manifest.Encode(&encoded, m); err != nil {
			t.Fatal(err)
		}
		if _, err := manifest.Decode(&encoded); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeExtensionStrictAndAtomic(t *testing.T) {
	type block struct {
		UI struct {
			Bundle string `json:"bundle"`
		} `json:"ui"`
	}
	var got block
	if err := manifest.DecodeExtension(json.RawMessage(`{"ui":{"bundle":"ui/index.js"}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.UI.Bundle != "ui/index.js" {
		t.Fatal("lost bundle")
	}
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"ui":{"Bundle":"other.js"}}`, `{"ui":{"bundle":"one.js","bundle":"two.js"}}`, `{"ui":{"bundle":0}}`, `{"ui":{"script":"bad.js"}}`} {
		t.Run(raw, func(t *testing.T) {
			before := got
			if err := manifest.DecodeExtension(json.RawMessage(raw), &got); err == nil {
				t.Fatal("accepted invalid extension")
			}
			if got != before {
				t.Fatal("modified destination on failure")
			}
		})
	}
	var nilBlock *block
	for _, dst := range []any{nil, nilBlock, block{}, new(string)} {
		if err := manifest.DecodeExtension(json.RawMessage(`{}`), dst); err == nil {
			t.Fatal("accepted invalid destination")
		}
	}
}
