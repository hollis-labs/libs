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
