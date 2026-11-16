package agentdir

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

func TestSDKStreamsDeltasBeforeCompletion(t *testing.T) {
	root := t.TempDir()
	dir := writeAgent(t, root, "stable", "durable", "kind: stable\nlifetime: on-demand\nseat_id: seat-1\n")
	definition := strings.Replace(definition("durable"), "profile: example", "profile: synthetic", 1)
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	fh := fakehost.New()
	release := make(chan struct{})
	fh.Model.Stream = func(ctx context.Context, _ llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
		out := make(chan llmtypes.StreamEvent)
		go func() {
			defer close(out)
			out <- llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "first "}
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			out <- llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "second"}
			out <- llmtypes.StreamEvent{Type: llmtypes.EventDone}
		}()
		return out, nil
	}
	factory := SDKFactory{
		Ports: func(_ context.Context, _ Snapshot, conversation string) (sdk.Ports, error) {
			fh.Binding.ConversationID = runtimecontract.ConversationID(conversation)
			return fh.Ports(), nil
		},
		StreamPorts: func(_ context.Context, _ Snapshot, conversation string, delta func(string) error) (sdk.Ports, error) {
			fh.Binding.ConversationID = runtimecontract.ConversationID(conversation)
			ports := fh.Ports()
			ports.Router, _ = sdk.NewRouter("fake", sdk.Route{Name: "fake", Model: "fake-model", Provider: TapProvider{Provider: fh.Model, Delta: delta}})
			return ports, nil
		},
		Input: func(_ context.Context, _ Snapshot, _ string, text string) (sdk.Input, error) {
			in := fh.Input()
			in.Text = text
			return in, nil
		},
	}
	h := Host{Root: root, Store: NewMemoryStore(), Factory: factory}
	server := httptest.NewServer(h.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post(server.URL+"/agents/stable/turns", "application/json", strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("status %d: %s", response.StatusCode, data)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, `"text":"first "`) {
			break
		}
		if strings.Contains(line, "run.finish") {
			t.Fatal("run completed before second provider delta")
		}
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), `"text":"second"`) || !strings.Contains(string(rest), "event: run.finish") {
		t.Fatalf("missing incremental completion: %s", rest)
	}
}
