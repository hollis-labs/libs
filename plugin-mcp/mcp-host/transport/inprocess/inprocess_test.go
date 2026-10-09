package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/mcp-host/config"
	pluginhost "github.com/hollis-labs/libs/plugin-mcp/plugin-host"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	sdksub "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func fixtureTools() []config.ToolManifest {
	return []config.ToolManifest{{
		Name:        "ping",
		Description: "Echoes back the given message.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		},
		Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
	}}
}

func selfExecFixtureConfig(t *testing.T, extraEnv map[string]string) *config.InprocessConfig {
	env := map[string]string{fixtureEnvVar: "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	return &config.InprocessConfig{
		Command:     os.Args[0],
		InitFactory: fixtureInitFactory(t),
		Env:         env,
		Tools:       fixtureTools(),
	}
}

func TestNew_HandshakeListToolsAndCallTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr, err := New(ctx, "clock", selfExecFixtureConfig(t, nil), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: unexpected error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("ListTools = %+v, want one tool named ping", tools)
	}
	if !tools[0].Annotations["readOnlyHint"].(bool) {
		t.Errorf("tools[0].Annotations = %+v, want readOnlyHint true (from the manifest, not a live call)", tools[0].Annotations)
	}

	res, err := tr.CallTool(ctx, "ping", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool result IsError=true: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Text == "" {
		t.Fatalf("CallTool result = %+v, want non-empty text content", res)
	}
}

func TestNew_InvalidCommand_FailsFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := New(ctx, "bogus", &config.InprocessConfig{Command: "/nonexistent/station-test-plugin", InitFactory: fixtureInitFactory(t)}, testLogger())
	if err == nil {
		t.Fatal("New: expected error for nonexistent command, got nil")
	}
}

func TestClose_StopsProcessAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr, err := New(ctx, "clock", selfExecFixtureConfig(t, nil), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: unexpected error: %v", err)
	}

	// ListTools is manifest-driven, not live — it must keep answering
	// from config even after Close, the same way a process-mode server's
	// advertised name doesn't depend on an open connection.
	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools after Close: unexpected error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("ListTools after Close = %+v, want one tool named ping", tools)
	}

	if _, err := tr.CallTool(ctx, "ping", nil); err == nil {
		t.Fatal("CallTool after Close: expected error, got nil")
	}
}

// TestSupervisedRestart_OnCrash kills the plugin subprocess out from
// under a live Transport and confirms plugin-host-driven recovery,
// mirroring the process-mode restart test.
func TestSupervisedRestart_OnCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	receipt := filepath.Join(t.TempDir(), "initialized")
	tr, err := New(ctx, "clock", selfExecFixtureConfig(t, map[string]string{fixtureInitializedFileVar: receipt}), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	if _, err := tr.CallTool(ctx, "ping", map[string]any{"message": "hi"}); err != nil {
		t.Fatalf("initial CallTool: unexpected error: %v", err)
	}

	pid := tr.Pid()
	if pid == 0 {
		t.Fatal("Pid() = 0, want a live process")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill plugin (pid %d): %v", pid, err)
	}

	// Recovery is checked via CallTool, not ListTools — ListTools is
	// manifest-driven now and would report success (and the same tool
	// list) whether or not the subprocess is actually alive.
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if tr.RestartCount() > 0 {
			if _, err := tr.CallTool(ctx, "ping", map[string]any{"message": "hi"}); err == nil {
				raw, err := os.ReadFile(receipt)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
				if len(lines) < 2 {
					t.Fatal("missing physical restarted Init receipt")
				}
				var first, last capability.RuntimeIdentity
				if json.Unmarshal([]byte(lines[0]), &first) != nil || json.Unmarshal([]byte(lines[len(lines)-1]), &last) != nil {
					t.Fatal("invalid incarnation receipt")
				}
				if first.HostInstance != last.HostInstance || first.OwnerID != last.OwnerID || last.OwnerGeneration <= first.OwnerGeneration {
					t.Fatalf("stale or substituted runtime on wire: first=%+v last=%+v", first, last)
				}
				return // recovered with genuine fresh owner input
			} else {
				lastErr = err
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("transport did not recover after kill within deadline; restarts=%d lastErr=%v", tr.RestartCount(), lastErr)
}

// TestSupervisedRestart_OnUnhealthy proves the proactive path: a plugin
// that stays alive but starts failing plugin/health gets killed and
// restarted by the periodic health poll, not just by a crash.
func TestSupervisedRestart_OnUnhealthy(t *testing.T) {
	origInterval, origTimeout := healthCheckInterval, healthCheckTimeout
	healthCheckInterval = 200 * time.Millisecond
	healthCheckTimeout = 2 * time.Second
	t.Cleanup(func() {
		healthCheckInterval, healthCheckTimeout = origInterval, origTimeout
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Fail every health check from the start (unhealthyAfter=0).
	tr, err := New(ctx, "clock", selfExecFixtureConfig(t, map[string]string{fixtureUnhealthyAfterVar: "0"}), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if tr.RestartCount() > 0 {
			return // the health poll forced a restart
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("transport did not restart on unhealthy status within deadline; restarts=%d", tr.RestartCount())
}

func TestSuperviseDisabled_DoesNotRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := selfExecFixtureConfig(t, nil)
	disabled := false
	cfg.Supervise = &disabled

	tr, err := New(ctx, "clock", cfg, testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	pid := tr.Pid()
	if pid == 0 {
		t.Fatal("Pid() = 0, want a live process")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill plugin (pid %d): %v", pid, err)
	}

	time.Sleep(1 * time.Second)
	if tr.RestartCount() != 0 {
		t.Fatalf("RestartCount() = %d, want 0 with supervision disabled", tr.RestartCount())
	}
	if _, err := tr.CallTool(ctx, "ping", nil); err == nil {
		t.Fatal("CallTool after unsupervised crash: expected error, got nil")
	}
}

// These are explicit host-owned test identities and roots, not production defaults.
func fixtureInitFactory(t *testing.T) config.InprocessInitFactory {
	t.Helper()
	root := t.TempDir()
	host, err := pluginhost.NewHostInstance()
	if err != nil {
		t.Fatal(err)
	}
	var generation atomic.Uint64
	return func(context.Context) (config.InprocessInitialization, error) {
		return config.InprocessInitialization{
			Params: sdksub.InitParams{PluginDir: root, DataDir: root, CacheDir: root,
				Config: map[string]string{}, LogLevel: "info",
				HostInfo:           sdksub.HostInfo{Version: "0.1.0", Protocol: sdksub.ProtocolVersion},
				CapabilityContract: capability.ContractVersion,
				Incarnation:        capability.RuntimeIdentity{HostInstance: host, OwnerID: "fixture", OwnerGeneration: generation.Add(1)},
				Grants:             capability.GrantSet{}},
			ExpectedID: "fixture", ExpectedVersion: "0.1.0",
		}, nil
	}
}

func TestInitializationInvalidNeverSpawns(t *testing.T) {
	cases := map[string]func(*config.InprocessInitialization){
		"invalid expected version": func(v *config.InprocessInitialization) { v.ExpectedVersion = "not-a-version" },
		"missing roots":            func(v *config.InprocessInitialization) { v.Params.DataDir = "" },
		"missing incarnation":      func(v *config.InprocessInitialization) { v.Params.Incarnation = capability.RuntimeIdentity{} },
		"implicit grants":          func(v *config.InprocessInitialization) { v.Params.Grants = nil },
		"wrong protocol":           func(v *config.InprocessInitialization) { v.Params.HostInfo.Protocol = 1 },
		"wrong contract":           func(v *config.InprocessInitialization) { v.Params.CapabilityContract = 0 },
		"reverse offer":            func(v *config.InprocessInitialization) { v.Params.HostServices = &sdksub.HostServices{} },
		"hooks offer": func(v *config.InprocessInitialization) {
			v.Params.HooksProfile = &sdksub.HooksProfile{HooksProfileVersion: 1}
		},
		"missing expected identity": func(v *config.InprocessInitialization) { v.ExpectedID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "spawned")
			cfg := selfExecFixtureConfig(t, map[string]string{fixtureStartedFileVar: marker})
			input, err := cfg.InitFactory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			mutate(&input)
			cfg.InitFactory = func(context.Context) (config.InprocessInitialization, error) { return input, nil }
			if tr, err := New(context.Background(), "fixture", cfg, testLogger()); err == nil {
				_ = tr.Close()
				t.Fatal("invalid initialization accepted")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("process existed before validation: %v", err)
			}
		})
	}
	cfg := selfExecFixtureConfig(t, nil)
	cfg.InitFactory = nil
	if _, err := New(context.Background(), "fixture", cfg, testLogger()); err == nil {
		t.Fatal("missing owner factory accepted")
	}
}

func TestInitializationMismatchNeverLoads(t *testing.T) {
	for _, change := range []string{"id", "version", "reverse", "protocol", "contract"} {
		t.Run(change, func(t *testing.T) {
			loaded := filepath.Join(t.TempDir(), "loaded")
			cfg := selfExecFixtureConfig(t, map[string]string{fixtureLoadedFileVar: loaded})
			if change != "id" && change != "version" {
				cfg.Env[fixtureRawInitVar] = change
			}
			factory := cfg.InitFactory
			cfg.InitFactory = func(ctx context.Context) (config.InprocessInitialization, error) {
				v, err := factory(ctx)
				if change == "id" {
					v.ExpectedID = "other"
				} else if change == "version" {
					v.ExpectedVersion = "other"
				}
				return v, err
			}
			if tr, err := New(context.Background(), "fixture", cfg, testLogger()); err == nil {
				_ = tr.Close()
				t.Fatal("mismatch accepted")
			}
			if _, err := os.Stat(loaded); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Load occurred before agreement: %v", err)
			}
		})
	}
}

func TestInitializationSnapshotAndFreshGeneration(t *testing.T) {
	factory := fixtureInitFactory(t)
	input, err := factory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	input.Params.Config["key"] = "original"
	input.Params.Identity = json.RawMessage(`{"caller":"original"}`)
	input.Params.Grants = capability.GrantSet{{GrantID: "fixture-grant", Name: "storage.read", SchemaVersion: 1,
		Scope: json.RawMessage(`{"keys":["original"]}`), HostInstance: input.Params.Incarnation.HostInstance,
		OwnerID: input.Params.Incarnation.OwnerID, OwnerGeneration: input.Params.Incarnation.OwnerGeneration,
		Audience: "fixture", IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2027-01-01T00:00:00Z", PolicyRevision: "fixture-policy"}}
	tr := &Transport{initFactory: func(context.Context) (config.InprocessInitialization, error) { return input, nil }}
	first, err := tr.prepareInitialization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	input.Params.Config["key"] = "changed"
	input.Params.Identity[11] = 'X'
	input.Params.Grants[0].Scope[10] = 'X'
	if string(first.Params.Identity) != `{"caller":"original"}` || string(first.Params.Grants[0].Scope) != `{"keys":["original"]}` {
		t.Fatal("raw grant or identity aliases owner snapshot")
	}
	if first.Params.Config["key"] != "original" {
		t.Fatal("owner config still aliases snapshot")
	}
	if _, err := tr.prepareInitialization(context.Background()); err == nil {
		t.Fatal("repeated incarnation accepted")
	}
	input.Params.Incarnation.OwnerGeneration++
	input.Params.Grants[0].OwnerGeneration = input.Params.Incarnation.OwnerGeneration
	if _, err := tr.prepareInitialization(context.Background()); err != nil {
		t.Fatal(err)
	}
	input.Params.Incarnation.OwnerGeneration--
	input.Params.Grants[0].OwnerGeneration = input.Params.Incarnation.OwnerGeneration
	if _, err := tr.prepareInitialization(context.Background()); err == nil {
		t.Fatal("stale incarnation accepted")
	}
	input.Params.Incarnation.OwnerGeneration += 2
	input.Params.Grants[0].OwnerGeneration = input.Params.Incarnation.OwnerGeneration
	input.Params.Incarnation.OwnerID = "other"
	input.Params.Grants[0].OwnerID = "other"
	if _, err := tr.prepareInitialization(context.Background()); err == nil {
		t.Fatal("owner replacement accepted")
	}
}

func TestSupervisionFactoryFailureOrStaleStopsBeforeSpawn(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			cfg := selfExecFixtureConfig(t, nil)
			input, err := cfg.InitFactory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int64
			failure := errors.New("owner initialization stopped")
			cfg.InitFactory = func(context.Context) (config.InprocessInitialization, error) {
				if calls.Add(1) > 1 && !stale {
					return config.InprocessInitialization{}, failure
				}
				return input, nil
			}
			tr, err := New(context.Background(), "fixture", cfg, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			if err := syscall.Kill(tr.Pid(), syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			select {
			case <-tr.doneCh:
			case <-time.After(5 * time.Second):
				t.Fatal("invalid owner initialization did not terminate supervision")
			}
			if calls.Load() != 2 || tr.Pid() != 0 {
				t.Fatalf("extra attempt or live process: calls=%d pid=%d", calls.Load(), tr.Pid())
			}
		})
	}
}

func TestCloseCancelsPendingRestartInitialization(t *testing.T) {
	cfg := selfExecFixtureConfig(t, nil)
	factory := cfg.InitFactory
	entered := make(chan struct{})
	var calls atomic.Int64
	cfg.InitFactory = func(ctx context.Context) (config.InprocessInitialization, error) {
		if calls.Add(1) == 1 {
			return factory(ctx)
		}
		close(entered)
		<-ctx.Done()
		return config.InprocessInitialization{}, ctx.Err()
	}
	tr, err := New(context.Background(), "fixture", cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if err := syscall.Kill(tr.Pid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("restart factory not entered")
	}
	closed := make(chan struct{})
	go func() { _ = tr.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel pending owner initialization")
	}
	if tr.Pid() != 0 || calls.Load() != 2 {
		t.Fatal("closed transport restarted")
	}
}

func TestDriverCancellationKeepsChildUsableAndCloseReaps(t *testing.T) {
	tr, err := New(context.Background(), "clock", selfExecFixtureConfig(t, nil), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	pid := tr.Pid()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := tr.CallTool(ctx, "wait", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call deadline: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := tr.CallTool(ctx2, "ping", nil); err != nil {
		t.Fatalf("child after cancelled call: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("driver did not reap child pid%d: %v", pid, err)
	}
}

// Controller recreation must obey the driver's canonical generation ledger,
// not merely the adapter's per-transport initialization validation.
func TestSharedDriverRefusesReusedGenerationAfterClose(t *testing.T) {
	cfg := selfExecFixtureConfig(t, nil)
	input, err := cfg.InitFactory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.InitFactory = func(context.Context) (config.InprocessInitialization, error) { return input, nil }
	tr, err := New(context.Background(), "clock", cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := New(context.Background(), "clock", cfg, testLogger())
	if err == nil {
		_ = again.Close()
		t.Fatal("recreated adapter reused retired generation")
	}
	if !errors.Is(err, pluginhost.ErrInvalidGeneration) {
		t.Fatalf("driver reservation cause lost: %v", err)
	}
}
