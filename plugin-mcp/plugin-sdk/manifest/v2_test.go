package manifest_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/manifest"
)

func testArtifact(name string, executable bool) manifest.Artifact {
	files := []manifest.ArtifactFile{{Path: name, SHA256: strings.Repeat("0", 64), Executable: executable}}
	digest, err := manifest.TreeDigest(files)
	if err != nil {
		panic(err)
	}
	return manifest.Artifact{Files: files, TreeSHA256: digest}
}
func nodeExample() manifest.Manifest {
	m := example()
	m.Server = manifest.Server{Runtime: "node", Entry: "bin/server.js", Engines: map[string]manifest.HostRange{"node": {Min: "22.0.0", Max: "26.99.99"}}}
	m.Artifact = testArtifact(m.Server.Entry, false)
	return m
}
func TestVersionOrderingAndBounds(t *testing.T) {
	ordered := []string{"0.9.0", "0.10.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "999999999999999999999999.0.0"}
	for i, a := range ordered {
		for j, b := range ordered {
			c, err := manifest.CompareVersions(a, b)
			if err != nil || (i < j && c >= 0) || (i == j && c != 0) || (i > j && c <= 0) {
				t.Fatalf("compare %s/%s: %d, %v", a, b, c, err)
			}
		}
	}
	c, err := manifest.CompareVersions("1.0.0+abc", "1.0.0+xyz")
	if err != nil || c != 0 {
		t.Fatal(c, err)
	}
	for _, r := range []manifest.HostRange{{}, {Min: "next"}, {Max: ""}, {Min: "0.10.0", Max: "0.9.0"}, {Min: "1.0.0", Max: "1.0.0-rc.1"}} {
		if r.Validate() == nil {
			t.Fatalf("accepted %#v", r)
		}
	}
	r := manifest.HostRange{Min: "0.9.0", Max: "0.10.0"}
	for _, v := range []string{"0.9.0", "0.10.0", "0.10.0+build"} {
		if err := r.Contains(v, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"", "dev", "0.8.9", "0.10.1", "0.10.0-rc.1"} {
		if r.Contains(v, false) == nil {
			t.Fatal("accepted", v)
		}
	}
	if err := r.Contains("0.10.0-rc.1", true); err != nil {
		t.Fatal(err)
	}
	if (manifest.HostRange{Max: "0.10.0"}).Contains("0.0.0", false) != nil || (manifest.HostRange{Min: "0.9.0"}).Contains("100.0.0", false) != nil {
		t.Fatal("unbounded side rejected")
	}
}
func TestCompatibilityFailClosed(t *testing.T) {
	m := nodeExample()
	m.Version = "0.0.1+local.dev"
	good := manifest.Compatibility{Hosts: map[string]string{"nanite": "0.1.0", "tangent": "1.0.0"}, Engines: map[string]string{"node": "22.0.0"}}
	if err := m.CheckCompatibility(good); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, value string
		engine      bool
	}{{"node", "", true}, {"node", "21.0.0", true}, {"node", "22.0.0-rc.1", true}, {"node", "not-a-version", true}, {"nanite", "", false}, {"tangent", "2.0.1", false}} {
		c := manifest.Compatibility{Hosts: map[string]string{"nanite": "0.1.0", "tangent": "1.0.0"}, Engines: map[string]string{"node": "22.0.0"}}
		if tc.engine {
			c.Engines[tc.name] = tc.value
		} else {
			c.Hosts[tc.name] = tc.value
		}
		if m.CheckCompatibility(c) == nil {
			t.Fatalf("accepted %s=%q", tc.name, tc.value)
		}
	}
}
func TestV2StructureAndStrictDecode(t *testing.T) {
	m := nodeExample()
	m.Hooks = []manifest.Hook{{Name: "context.pre_compact", Priority: new(0), Mode: "bail", Timeout: 1000, OnError: "closed"}}
	m.UI = &manifest.UI{Bundle: "ui/index.js", Stylesheet: "ui/style.css", Isolation: "sandboxed-frame"}
	m.Artifact.Files = append(m.Artifact.Files, manifest.ArtifactFile{Path: m.UI.Bundle, SHA256: strings.Repeat("1", 64)}, manifest.ArtifactFile{Path: m.UI.Stylesheet, SHA256: strings.Repeat("2", 64)})
	m.Artifact.TreeSHA256, _ = manifest.TreeDigest(m.Artifact.Files)
	var buf bytes.Buffer
	if err := manifest.Encode(&buf, m); err != nil {
		t.Fatal(err)
	}
	got, err := manifest.Decode(&buf)
	wantJSON, _ := json.Marshal(m)
	gotJSON, _ := json.Marshal(got)
	if err != nil || !bytes.Equal(wantJSON, gotJSON) {
		t.Fatal(err)
	}
	if got.Hooks[0].EffectivePriority() != 0 || (manifest.Hook{}).EffectivePriority() != 10 {
		t.Fatal("priority presence lost")
	}
	mutations := []func(*manifest.Manifest){
		func(m *manifest.Manifest) { m.Protocol = 1 }, func(m *manifest.Manifest) { m.Server.Runtime = "python" }, func(m *manifest.Manifest) { m.Server.Engines = nil },
		func(m *manifest.Manifest) { m.Server.Engines["node"] = manifest.HostRange{} },
		func(m *manifest.Manifest) {
			m.Server.Engines["node"] = manifest.HostRange{Min: "24.0.0", Max: "22.0.0"}
		},
		func(m *manifest.Manifest) { m.Server.Engines = map[string]manifest.HostRange{"deno": {Min: "2.0.0"}} },
		func(m *manifest.Manifest) { m.Server.Entry = "bin/launch" }, func(m *manifest.Manifest) { m.Server.Entry = "dist/server.js" }, func(m *manifest.Manifest) { m.Artifact.TreeSHA256 = strings.Repeat("f", 64) },
		func(m *manifest.Manifest) {
			m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "guess", Timeout: 1, OnError: "open"}}
		},
		func(m *manifest.Manifest) {
			m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "parallel", OnError: "open"}}
		},
		func(m *manifest.Manifest) {
			m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "parallel", Timeout: 1}}
		},
		func(m *manifest.Manifest) {
			m.Hooks = []manifest.Hook{{Name: "bad", Mode: "parallel", Timeout: 1, OnError: "open"}}
		},
		func(m *manifest.Manifest) { m.UI = &manifest.UI{Bundle: "ui/index.js", Isolation: "automatic"} },
	}
	for i, change := range mutations {
		n := nodeExample()
		change(&n)
		if n.Validate() == nil {
			t.Fatalf("accepted mutation %d", i)
		}
	}
	raw, _ := json.Marshal(nodeExample())
	for _, bad := range []string{
		strings.Replace(string(raw), `"entry":`, `"flags":[],"entry":`, 1), strings.Replace(string(raw), `"entry":`, `"Entry":`, 1),
		strings.Replace(string(raw), `"min":"22.0.0"`, `"min":"22.0.0","range":"*"`, 1),
		strings.Replace(string(raw), `"path":`, `"symlink":"target","path":`, 1),
		strings.Replace(string(raw), `"min":"22.0.0"`, `"min":null,"max":"24.0.0"`, 1),
		strings.Replace(string(raw), `"engines":`, `"engines":null,"unused":`, 1),
		strings.Replace(string(raw), `"path":`, `"executable":null,"path":`, 1),
		strings.Replace(string(raw), `"runtime":"subprocess"`, `"entrypoint":{"command":"bin/plugin"},"runtime":"subprocess"`, 1),
	} {
		if _, err := manifest.Decode(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted unknown key", bad)
		}
	}
	for _, runtime := range []string{"deno", "bun", "binary"} {
		n := nodeExample()
		n.Server.Runtime = runtime
		n.Server.Engines = map[string]manifest.HostRange{runtime: {Min: "1.0.0"}}
		if runtime == "binary" {
			n.Artifact.Files[0].Executable = true
			n.Artifact.TreeSHA256, _ = manifest.TreeDigest(n.Artifact.Files)
		}
		if err := n.Validate(); err != nil {
			t.Fatal(runtime, err)
		}
	}
}
func TestBundlePaths(t *testing.T) {
	for _, p := range []string{"", ".", "bin/../server.js", "bin//server.js", "./bin/server.js", "/bin/server.js", `bin\server.js`, "bin/server.js\x00", "bin/server.js ", "bin/server.js.", "C:/server.js", "bin/$(x).js"} {
		m := nodeExample()
		m.Server.Entry = p
		if m.Validate() == nil {
			t.Fatal("accepted", p)
		}
	}
}
func TestTreeDigestVectorAndAmbiguity(t *testing.T) {
	files := []manifest.ArtifactFile{{Path: "ui/index.js", SHA256: strings.Repeat("1", 64)}, {Path: "bin/server", SHA256: strings.Repeat("0", 64), Executable: true}}
	// Independent wire bytes: domain, BE lengths, literal paths, raw digest, mode.
	wire := []byte("plugin-sdk-artifact-v1\x00\x00\x00\x00\x0abin/server")
	wire = append(wire, make([]byte, 32)...)
	wire = append(wire, 1, 0, 0, 0, 11)
	wire = append(wire, []byte("ui/index.js")...)
	wire = append(wire, bytes.Repeat([]byte{0x11}, 32)...)
	wire = append(wire, 0)
	sum := sha256.Sum256(wire)
	want := hex.EncodeToString(sum[:])
	got, err := manifest.TreeDigest(files)
	if err != nil || got != want {
		t.Fatal(got, want, err)
	}
	slices.Reverse(files)
	again, _ := manifest.TreeDigest(files)
	if again != got {
		t.Fatal("order changed digest")
	}
	files[0].Executable = false
	changed, _ := manifest.TreeDigest(files)
	if changed == got {
		t.Fatal("mode not bound")
	}
	for _, files := range [][]manifest.ArtifactFile{nil, {{Path: "plugin.yaml", SHA256: strings.Repeat("0", 64)}}, {{Path: "bin/a", SHA256: strings.Repeat("0", 64)}, {Path: "BIN/A", SHA256: strings.Repeat("0", 64)}}, {{Path: "bin/a", SHA256: "bad"}}, {{Path: "bin", SHA256: strings.Repeat("0", 64)}, {Path: "bin/a", SHA256: strings.Repeat("0", 64)}}} {
		if _, err := manifest.TreeDigest(files); err == nil {
			t.Fatal("accepted ambiguous inventory", files)
		}
	}
}
func stagedBundle(t *testing.T) (manifest.Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("console.log('worker');\n")
	sum := sha256.Sum256(data)
	m := nodeExample()
	m.Artifact.Files[0].SHA256 = hex.EncodeToString(sum[:])
	m.Artifact.TreeSHA256, _ = manifest.TreeDigest(m.Artifact.Files)
	if err := os.WriteFile(filepath.Join(dir, "bin/server.js"), data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, manifest.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Encode(f, m); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return m, dir
}
func TestVerifyBundleBytesAndSymlinks(t *testing.T) {
	for _, tc := range []string{"ok", "tamper", "missing", "extra", "mode", "file symlink", "dir symlink", "manifest symlink", "manifest change", "manifest missing"} {
		t.Run(tc, func(t *testing.T) {
			m, dir := stagedBundle(t)
			entry := filepath.Join(dir, "bin/server.js")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch tc {
			case "tamper":
				must(os.WriteFile(entry, []byte("evil"), 0600))
			case "missing":
				must(os.Remove(entry))
			case "extra":
				must(os.WriteFile(filepath.Join(dir, "extra"), nil, 0600))
			case "mode":
				must(os.Chmod(entry, 0700))
			case "file symlink":
				must(os.Rename(entry, filepath.Join(dir, "target")))
				must(os.Symlink("../target", entry))
			case "dir symlink":
				must(os.Rename(filepath.Join(dir, "bin"), filepath.Join(dir, "real")))
				must(os.Symlink("real", filepath.Join(dir, "bin")))
			case "manifest symlink":
				must(os.Rename(filepath.Join(dir, manifest.Filename), filepath.Join(dir, "real.yaml")))
				must(os.Symlink("real.yaml", filepath.Join(dir, manifest.Filename)))
			case "manifest change":
				m.Name = "different"
			case "manifest missing":
				must(os.Remove(filepath.Join(dir, manifest.Filename)))
			}
			err := m.VerifyBundle(dir)
			if tc == "ok" && err != nil {
				t.Fatal(err)
			}
			if tc != "ok" && err == nil {
				t.Fatal("accepted", tc)
			}
		})
	}
}

func TestNodeFloorWithUnboundedMin(t *testing.T) {
	m := nodeExample()
	m.Server.Engines["node"] = manifest.HostRange{Max: "24.0.0"}
	c := manifest.Compatibility{Hosts: map[string]string{"nanite": "0.1.0", "tangent": "1.0.0"}, Engines: map[string]string{"node": "22.0.0"}}
	if err := m.CheckCompatibility(c); err != nil {
		t.Fatal(err)
	}
	c.Engines["node"] = "21.99.99"
	if err := m.CheckCompatibility(c); err == nil {
		t.Fatal("accepted Node below policy floor with unbounded min")
	}
}

func TestAdditionalV2Rejections(t *testing.T) {
	for _, change := range []func(*manifest.Manifest){
		func(m *manifest.Manifest) { m.UI = &manifest.UI{Bundle: "ui/index.js", Isolation: "sandboxed-frame"} },
		func(m *manifest.Manifest) { m.UI = &manifest.UI{Bundle: "../index.js", Isolation: "sandboxed-frame"} },
		func(m *manifest.Manifest) {
			m.UI = &manifest.UI{Bundle: "ui/index.js", Stylesheet: "ui/a.js", Isolation: "sandboxed-frame"}
		},
		func(m *manifest.Manifest) {
			m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "parallel", Timeout: 1, OnError: "open"}, {Name: "session.end", Mode: "parallel", Timeout: 1, OnError: "open"}}
		},
		func(m *manifest.Manifest) { m.Server.Engines["extra"] = manifest.HostRange{Min: "1.0.0"} },
		func(m *manifest.Manifest) {
			m.Artifact.Files[0].Executable = true
			m.Artifact.TreeSHA256, _ = manifest.TreeDigest(m.Artifact.Files)
		},
	} {
		m := nodeExample()
		change(&m)
		if m.Validate() == nil {
			t.Fatal("accepted invalid declaration")
		}
	}
	m, dir := stagedBundle(t)
	link := filepath.Join(t.TempDir(), "bundle")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if m.VerifyBundle(link) == nil {
		t.Fatal("accepted symlink root")
	}
}

func TestHookViewAndOnceDeclarations(t *testing.T) {
	for _, once := range []*bool{nil, new(false), new(true)} {
		for _, view := range []*string{nil, new("summary"), new("reasoning_blind"), new("host.summary-v2")} {
			m := nodeExample()
			m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "sequential", Timeout: 1000, OnError: "open", Once: once, View: view}}
			var out bytes.Buffer
			if err := manifest.Encode(&out, m); err != nil {
				t.Fatal(err)
			}
			got, err := manifest.Decode(&out)
			if err != nil {
				t.Fatal(err)
			}
			h := got.Hooks[0]
			if (h.Once == nil) != (once == nil) || (once != nil && *h.Once != *once) || (h.View == nil) != (view == nil) || (view != nil && *h.View != *view) {
				t.Fatal("option presence or value lost")
			}
		}
	}
	m := nodeExample()
	m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "sequential", Timeout: 1000, OnError: "open"}}
	raw, _ := json.Marshal(m)
	for _, option := range []string{`"once":"true"`, `"once":1`, `"once":null`, `"Once":true`, `"once":true,"once":false`, `"view":""`, `"view":" "`, `"view":"bad token"`, `"view":"bad\nview"`, `"view":null`, `"view":1`, `"View":"summary"`, `"view":"a","view":"b"`} {
		bad := strings.Replace(string(raw), `"on_error":"open"`, `"on_error":"open",`+option, 1)
		if _, err := manifest.Decode(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted", option)
		}
	}
	for _, view := range []string{"", " ", "bad token", "bad\nview"} {
		m.Hooks[0].View = new(view)
		if err := m.Validate(); err == nil {
			t.Fatal("accepted view", view)
		}
	}
}
func TestUIIsolationPreferences(t *testing.T) {
	m := nodeExample()
	m.UI = &manifest.UI{Bundle: "ui/index.js"}
	m.Artifact.Files = append(m.Artifact.Files, manifest.ArtifactFile{Path: "ui/index.js", SHA256: strings.Repeat("1", 64)})
	m.Artifact.TreeSHA256, _ = manifest.TreeDigest(m.Artifact.Files)
	for _, isolation := range []string{"sandboxed-frame", "main-origin"} {
		m.UI.Isolation = isolation
		var out bytes.Buffer
		if err := manifest.Encode(&out, m); err != nil {
			t.Fatal(err)
		}
		got, err := manifest.Decode(&out)
		if err != nil || got.UI.Isolation != isolation {
			t.Fatal("isolation preference lost", err)
		}
	}
	for _, isolation := range []string{"iframe", "shared", "automatic", ""} {
		m.UI.Isolation = isolation
		if err := m.Validate(); err == nil {
			t.Fatal("accepted isolation", isolation)
		}
	}
}
