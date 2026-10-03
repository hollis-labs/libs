package tether

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in contract check against a separately built current-main daemon. The
// binary is the only supplied artifact: this test always creates its own HOME,
// catalog, state and socket, and never launches a provider/model process.
func TestRegistryRealRenamedDaemon(t *testing.T) {
	binary := os.Getenv("GO_TETHER_CLIENT_TEST_DAEMON")
	if binary == "" {
		t.Skip("set GO_TETHER_CLIENT_TEST_DAEMON to a current-main tether binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the socket path short enough for Unix sockaddr limits.
	home, err := os.MkdirTemp("", "tc0653-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	t.Setenv("OTEL_SDK_DISABLED", "true")
	logFile, err := os.Create(filepath.Join(home, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, "daemon", "run", "--catalog", filepath.Join(home, ".tether", "catalog"))
	cmd.Dir = home
	cmd.Env = os.Environ()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})
	// Exercise the actual default, not an explicit address that could hide a
	// stale default socket. Its expansion is guaranteed beneath this temp HOME.
	c := MustNew("")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := c.Ping(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(logFile.Name())
			t.Fatalf("temp daemon never became healthy: %s", log)
		}
		time.Sleep(25 * time.Millisecond)
	}
	registered, err := c.Registry().Register(ctx, RegistryKindAgent, RegistryProfile{DisplayName: "client-rename-smoke", Role: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if registered.TetherInstanceID == "" || !strings.HasPrefix(registered.URN, "msg://agent/agent-mux/agt_") {
		t.Fatalf("registration lost renamed instance or durable URN: %+v", registered)
	}
	found, err := c.Registry().Lookup(ctx, registered.URN)
	if err != nil {
		t.Fatal(err)
	}
	if found.URN != registered.URN || found.TetherInstanceID != registered.TetherInstanceID {
		t.Fatalf("lookup mismatch: registered=%+v found=%+v", registered, found)
	}
	// Confirm the real server emits only the renamed wire key.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/registry/agents/"+url.PathEscape(registered.URN), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var wire map[string]any
	if err := json.NewDecoder(response.Body).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || wire["tether_instance_id"] != registered.TetherInstanceID {
		t.Fatalf("wire response: %d %v", response.StatusCode, wire)
	}
	if _, old := wire["mux_instance_id"]; old {
		t.Fatalf("real server emitted old instance key: %v", wire)
	}
	t.Logf("real daemon default socket=%s; register/lookup URN=%s tether_instance_id=%q; legacy JSON key absent", expandListenAddr(DefaultListenAddr), found.URN, found.TetherInstanceID)
}
