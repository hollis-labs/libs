package plugins

import (
	"context"
	"os"
	"testing"
	"time"

	pluginhost "github.com/hollis-labs/libs/plugin-mcp/plugin-host"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-host/pluginhosttest"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

func TestMain(m *testing.M) { pluginhosttest.MaybeRunFixture(); os.Exit(m.Run()) }
func fixtureSpec(t *testing.T, behavior string) pluginhost.Spec {
	command, env := pluginhosttest.FixtureCommand(behavior, t.TempDir())
	return pluginhost.Spec{ID: "fixture", Command: command, Env: env, HandshakeTimeout: time.Second, UnloadTimeout: time.Second, ReapTimeout: time.Second, Init: subprocess.InitParams{HostInfo: subprocess.HostInfo{Version: "1.0.0", Protocol: subprocess.ProtocolVersion}, PluginDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), CapabilityContract: capability.ContractVersion, Incarnation: capability.RuntimeIdentity{HostInstance: "test", OwnerID: "fixture", OwnerGeneration: 1}, Grants: capability.GrantSet{}}}
}
func TestInventoryLifetimeAndFailedAdmission(t *testing.T) {
	spec := fixtureSpec(t, pluginhosttest.BehaviourEcho)
	inventory := NewInventory([]pluginhost.Spec{spec})
	ctx, cancel := context.WithCancel(context.Background())
	if err := inventory.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	p := inventory.processes[0]
	select {
	case <-p.Exited():
		t.Fatal("boot cancellation killed plugin")
	case <-time.After(20 * time.Millisecond):
	}
	if err := inventory.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Exited():
	default:
		t.Fatal("Stop did not reap")
	}
	bad := NewInventory([]pluginhost.Spec{fixtureSpec(t, pluginhosttest.BehaviourInitError)})
	if err := bad.Start(context.Background()); err == nil {
		t.Fatal("accepted failed handshake")
	}
	if len(bad.processes) != 0 {
		t.Fatal("failed start retained process")
	}
}
