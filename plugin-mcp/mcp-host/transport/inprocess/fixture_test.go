package inprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	sdksub "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

const (
	fixtureInitializedFileVar = "STATION_TEST_FIXTURE_INITIALIZED_FILE"
	fixtureStartedFileVar     = "STATION_TEST_FIXTURE_STARTED_FILE"
	fixtureRawInitVar         = "STATION_TEST_FIXTURE_RAW_INIT"
	fixtureLoadedFileVar      = "STATION_TEST_FIXTURE_LOADED_FILE"
	fixtureEnvVar             = "STATION_TEST_FIXTURE_PLUGIN"
	fixtureUnhealthyAfterVar  = "STATION_TEST_FIXTURE_UNHEALTHY_AFTER"
)

func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnvVar) != "" {
		runFixturePlugin()
		return
	}
	os.Exit(m.Run())
}

// runFixturePlugin uses plugin-sdk/subprocess.Serve exactly as
// documented — Plugin + MCPHandler + HealthChecker. That's now valid:
// this package never sends mcp/list_tools (see the package doc), which
// was the one method Serve's own dispatch has no case for.
func runFixturePlugin() {
	if file := os.Getenv(fixtureStartedFileVar); file != "" {
		if err := os.WriteFile(file, []byte("started"), 0600); err != nil {
			os.Exit(2)
		}
	}
	if mode := os.Getenv(fixtureRawInitVar); mode != "" {
		runRawInitFixture(mode)
		return
	}
	unhealthyAfter := -1
	if v := os.Getenv(fixtureUnhealthyAfterVar); v != "" {
		fmt.Sscanf(v, "%d", &unhealthyAfter)
	}
	if err := sdksub.Serve(&fixturePlugin{unhealthyAfter: unhealthyAfter}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type fixturePlugin struct {
	unhealthyAfter int
	healthCalls    atomic.Int64
}

func (p *fixturePlugin) Init(ctx context.Context, params sdksub.InitParams) (sdksub.InitResult, error) {
	if path := os.Getenv(fixtureInitializedFileVar); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return sdksub.InitResult{}, err
		}
		err = json.NewEncoder(file).Encode(params.Incarnation)
		closeErr := file.Close()
		if err != nil {
			return sdksub.InitResult{}, err
		}
		if closeErr != nil {
			return sdksub.InitResult{}, closeErr
		}
	}
	return sdksub.InitResult{ID: "fixture", Name: "Fixture Plugin", Version: "test", Protocol: sdksub.ProtocolVersion, CapabilityContract: 1}, nil
}

func (p *fixturePlugin) Load(ctx context.Context) (sdksub.LoadResult, error) {
	if file := os.Getenv(fixtureLoadedFileVar); file != "" {
		if err := os.WriteFile(file, []byte("loaded"), 0600); err != nil {
			return sdksub.LoadResult{}, err
		}
	}
	return sdksub.LoadResult{}, nil
}

func (p *fixturePlugin) Unload(ctx context.Context) error { return nil }

func (p *fixturePlugin) Health(ctx context.Context) (sdksub.HealthStatus, error) {
	calls := p.healthCalls.Add(1)
	if p.unhealthyAfter >= 0 && calls > int64(p.unhealthyAfter) {
		return sdksub.HealthStatus{OK: false, Message: "forced unhealthy"}, nil
	}
	return sdksub.HealthStatus{OK: true}, nil
}

func (p *fixturePlugin) MCPCallTool(ctx context.Context, req sdksub.MCPCallRequest) (sdksub.MCPCallResult, error) {
	msg, _ := req.Arguments["message"].(string)
	payload, err := json.Marshal(map[string]any{"pong": "pong:" + msg})
	if err != nil {
		return sdksub.MCPCallResult{}, err
	}
	return sdksub.MCPCallResult{Content: payload}, nil
}

// A deliberately unsolicited or invalid Init reply proves the host refuses
// the physical handshake before publishing Load. Positive tests use real Serve.
func runRawInitFixture(mode string) {
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var request sdksub.RPCRequest
		if err := json.Unmarshal(scan.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		result := map[string]any{}
		if request.Method == sdksub.MethodInit {
			result = map[string]any{"id": "fixture", "name": "fixture", "version": "test", "description": "", "protocol": 2, "capability_contract": 1}
			switch mode {
			case "reverse":
				result["reverse_rpc_version"] = 1
			case "protocol":
				result["protocol"] = 1
			case "contract":
				delete(result, "capability_contract")
			}
		}
		if request.Method == sdksub.MethodLoad {
			if path := os.Getenv(fixtureLoadedFileVar); path != "" {
				if err := os.WriteFile(path, []byte("loaded"), 0600); err != nil {
					os.Exit(2)
				}
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(2)
		}
	}
}
