package subprocess

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestInitParamsRoundtripAllFields(t *testing.T) {
	in := InitParams{
		PluginDir: "/plugins/foo",
		DataDir:   "/data/foo",
		CacheDir:  "/cache/foo",
		Config:    map[string]string{"k": "v"},
		LogLevel:  "debug",
		HostInfo:  HostInfo{Version: "1.2.3", Protocol: 1},
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out InitParams
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.PluginDir != in.PluginDir {
		t.Errorf("PluginDir = %q, want %q", out.PluginDir, in.PluginDir)
	}
	if out.DataDir != in.DataDir {
		t.Errorf("DataDir = %q, want %q", out.DataDir, in.DataDir)
	}
	if out.CacheDir != in.CacheDir {
		t.Errorf("CacheDir = %q, want %q", out.CacheDir, in.CacheDir)
	}
	if out.LogLevel != in.LogLevel {
		t.Errorf("LogLevel = %q, want %q", out.LogLevel, in.LogLevel)
	}
	if out.Config["k"] != "v" {
		t.Errorf("Config[k] = %q, want %q", out.Config["k"], "v")
	}
	if out.HostInfo.Version != in.HostInfo.Version {
		t.Errorf("HostInfo.Version = %q, want %q", out.HostInfo.Version, in.HostInfo.Version)
	}
}

// TestInitParamsForwardCompatV011 verifies that a v0.1.1-shaped payload
// (no data_dir / cache_dir / log_level) decodes cleanly into the v0.1.2
// struct, leaving the new fields at their zero values.
func TestInitParamsForwardCompatV011(t *testing.T) {
	raw := `{
		"plugin_dir": "/plugins/foo",
		"config": {"k": "v"},
		"host_info": {"version": "1.1.0", "protocol": 1}
	}`

	var out InitParams
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.PluginDir != "/plugins/foo" {
		t.Errorf("PluginDir = %q", out.PluginDir)
	}
	if out.DataDir != "" {
		t.Errorf("DataDir = %q, want empty", out.DataDir)
	}
	if out.CacheDir != "" {
		t.Errorf("CacheDir = %q, want empty", out.CacheDir)
	}
	if out.LogLevel != "" {
		t.Errorf("LogLevel = %q, want empty", out.LogLevel)
	}
}

func TestResolvedDataDirPopulated(t *testing.T) {
	p := InitParams{DataDir: "/data/foo"}
	got, err := p.ResolvedDataDir()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "/data/foo" {
		t.Errorf("got %q, want %q", got, "/data/foo")
	}
}

func TestResolvedDataDirEmptyReturnsError(t *testing.T) {
	p := InitParams{}
	if _, err := p.ResolvedDataDir(); !errors.Is(err, ErrNoDataDir) {
		t.Errorf("err = %v, want ErrNoDataDir", err)
	}
}

func TestResolvedCacheDirPopulated(t *testing.T) {
	p := InitParams{CacheDir: "/cache/foo"}
	got, err := p.ResolvedCacheDir()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "/cache/foo" {
		t.Errorf("got %q, want %q", got, "/cache/foo")
	}
}

func TestResolvedCacheDirEmptyFallsBackToTempDir(t *testing.T) {
	p := InitParams{}
	got, err := p.ResolvedCacheDir()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != os.TempDir() {
		t.Errorf("got %q, want os.TempDir() %q", got, os.TempDir())
	}
}

// --- v0.2.0 wire types (B.10 surface) ---

func TestMCPCallRequestRoundtrip(t *testing.T) {
	in := MCPCallRequest{
		ToolName:  "summarize",
		Arguments: map[string]interface{}{"text": "hi", "max": float64(10)},
		SessionID: "sess-1",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MCPCallRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ToolName != in.ToolName || out.SessionID != in.SessionID {
		t.Errorf("roundtrip lost scalars: %+v", out)
	}
	if out.Arguments["text"] != "hi" {
		t.Errorf("arguments lost: %+v", out.Arguments)
	}
}

func TestMCPCallResultRoundtrip(t *testing.T) {
	in := MCPCallResult{
		Content: json.RawMessage(`{"text":"ok"}`),
		IsError: false,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MCPCallResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(out.Content) != `{"text":"ok"}` {
		t.Errorf("Content lost: %q", string(out.Content))
	}
}

func TestHTTPRequestResponseRoundtrip(t *testing.T) {
	req := HTTPRequest{
		Method:  "POST",
		Path:    "/x",
		Query:   map[string]string{"a": "1"},
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(`{"ok":true}`),
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	var gotReq HTTPRequest
	if err := json.Unmarshal(b, &gotReq); err != nil {
		t.Fatalf("unmarshal req: %v", err)
	}
	if gotReq.Method != "POST" || gotReq.Path != "/x" || string(gotReq.Body) != `{"ok":true}` {
		t.Errorf("req roundtrip lost data: %+v", gotReq)
	}

	resp := HTTPResponse{
		Status:  201,
		Headers: map[string]string{"Location": "/x/1"},
		Body:    []byte("created"),
	}
	b, err = json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var gotResp HTTPResponse
	if err := json.Unmarshal(b, &gotResp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if gotResp.Status != 201 || string(gotResp.Body) != "created" {
		t.Errorf("resp roundtrip lost data: %+v", gotResp)
	}
}

func TestMigrateParamsResultRoundtrip(t *testing.T) {
	params := MigrateParams{FromVersion: "1.0.0", ToVersion: "1.1.0", DataDir: "/data/foo"}
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var gotP MigrateParams
	if err := json.Unmarshal(b, &gotP); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if gotP != params {
		t.Errorf("params roundtrip: got %+v want %+v", gotP, params)
	}

	res := MigrateResult{Notes: []string{"applied 1→2", "backfilled column"}}
	b, err = json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal res: %v", err)
	}
	var gotR MigrateResult
	if err := json.Unmarshal(b, &gotR); err != nil {
		t.Fatalf("unmarshal res: %v", err)
	}
	if len(gotR.Notes) != 2 || gotR.Notes[0] != "applied 1→2" {
		t.Errorf("res roundtrip lost notes: %+v", gotR)
	}

	// Empty MigrateResult — "no-op migration" — must marshal cleanly.
	b, err = json.Marshal(MigrateResult{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("empty MigrateResult marshaled to %q, want %q", string(b), "{}")
	}
}
