package federation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cfgFixture struct {
	dir    string
	self   string // fingerprint of the install's own certificate
	peerFP string
}

func writeConfig(t *testing.T, body string) (path string, fx cfgFixture) {
	t.Helper()
	dir := t.TempDir()
	id, peerID := validIdentity(t), validIdentity(t)
	writeIdentityPEM(t, dir, "self", id)
	fx = cfgFixture{dir: dir, self: fp(id), peerFP: fp(peerID)}
	body = strings.ReplaceAll(body, "@PEER@", fx.peerFP)
	path = filepath.Join(dir, "federation.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, fx
}

const goodConfig = `{
  "listen_addr": "127.0.0.1:8443",
  "local_authorities": ["home"],
  "identity": {"cert_file": "self.crt", "key_file": "self.key"},
  "peers": [{"label": "other", "fingerprints": ["@PEER@"], "authorities": ["away"]}],
  "foreign_routes": [{"authority": "away", "endpoint": "https://other.example:8443", "server_pins": ["@PEER@"]}]
}`

func TestLoadConfigAbsentFileMeansStandalone(t *testing.T) {
	c, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json"))
	if c != nil || err != nil {
		t.Fatalf("absent config = %v, %v; want (nil, nil): federation is off, nothing runs", c, err)
	}
	f, err := Enable(filepath.Join(t.TempDir(), "nope.json"), newSpy())
	if f != nil || err != nil {
		t.Fatalf("Enable with no config = %v, %v", f, err)
	}
}

func TestLoadConfigValid(t *testing.T) {
	path, fx := writeConfig(t, goodConfig)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "127.0.0.1:8443" || len(c.LocalAuthorities) != 1 || c.peers.PinCount() != 1 || len(c.ForeignRoutes) != 1 {
		t.Fatalf("config = %+v", c)
	}
	if fp(c.identity) != fx.self {
		t.Error("the identity was not loaded from the paths, relative to the config file")
	}
	ops := c.OpSet()
	if ops.Allows(OpInbox) || ops.Allows(OpSubscribe) || !ops.Allows(OpSend) {
		t.Errorf("an absent ops field must mean the default set, got %v", ops)
	}
	ops[OpInbox] = true
	if c.OpSet().Allows(OpInbox) {
		t.Error("OpSet must return a copy")
	}
}

func TestLoadConfigOps(t *testing.T) {
	path, _ := writeConfig(t, strings.Replace(goodConfig, `"peers"`, `"ops": ["send","get","inbox"], "peers"`, 1))
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.OpSet(); !got.Allows(OpInbox) || got.Allows(OpThread) || !got.Allows(OpGet) {
		t.Errorf("ops = %v", got)
	}

	for name, ops := range map[string]string{"unknown": `["send","sned"]`, "empty": `[]`} {
		p, _ := writeConfig(t, strings.Replace(goodConfig, `"peers"`, `"ops": `+ops+`, "peers"`, 1))
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("ops %s: accepted", name)
		}
	}
}

func TestLoadConfigRejectsInvalidFilesLoudly(t *testing.T) {
	tests := map[string]struct{ from, to string }{
		"malformed JSON": {"{", ""},
		"unknown key (a typo in a security config)": {`"local_authorities"`, `"local_authoritis_typo": [], "local_authorities"`},
		"no listen_addr":              {`"listen_addr": "127.0.0.1:8443",`, ``},
		"no local authorities":        {`"local_authorities": ["home"]`, `"local_authorities": []`},
		"empty local authority":       {`["home"]`, `[""]`},
		"duplicate local authority":   {`["home"]`, `["home","home"]`},
		"no identity":                 {`"identity": {"cert_file": "self.crt", "key_file": "self.key"},`, ``},
		"missing key file":            {`"self.key"`, `"absent.key"`},
		"peer without authority":      {`"authorities": ["away"]`, `"authorities": []`},
		"peer with bad fingerprint":   {`"fingerprints": ["@PEER@"]`, `"fingerprints": ["xyz"]`},
		"route with http endpoint":    {`https://other.example`, `http://other.example`},
		"route without host":          {`"https://other.example:8443"`, `"https://"`},
		"route without pins":          {`"server_pins": ["@PEER@"]`, `"server_pins": []`},
		"route with bad pin":          {`"server_pins": ["@PEER@"]`, `"server_pins": ["nope"]`},
		"route for a local authority": {`"authority": "away"`, `"authority": "home"`},
	}
	for name, tc := range tests {
		body := goodConfig
		if tc.from == "{" {
			body = "{"
		} else {
			if !strings.Contains(body, tc.from) {
				t.Fatalf("%s: test is stale, %q not in the fixture", name, tc.from)
			}
			body = strings.Replace(body, tc.from, tc.to, 1)
		}
		path, _ := writeConfig(t, body)
		if c, err := LoadConfig(path); err == nil || c != nil {
			t.Errorf("%s: accepted (%v)", name, err)
		}
		if f, err := Enable(path, newSpy()); err == nil || f != nil {
			t.Errorf("%s: Enable accepted", name)
		}
	}
	// duplicate route authority
	dup := strings.Replace(goodConfig, `"foreign_routes": [`, `"foreign_routes": [{"authority": "away", "endpoint": "https://x.example", "server_pins": ["@PEER@"]},`, 1)
	if p, _ := writeConfig(t, dup); true {
		if _, err := LoadConfig(p); err == nil {
			t.Error("a route authority declared twice was accepted")
		}
	}
}
