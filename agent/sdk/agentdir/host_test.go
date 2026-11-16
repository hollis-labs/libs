package agentdir

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

type fakeFactory struct {
	mu        sync.Mutex
	seen      []string
	block     chan struct{}
	started   chan struct{}
	startOnce sync.Once
}

type fakeRunner struct {
	f  *fakeFactory
	s  Snapshot
	id string
}

func (f *fakeFactory) New(_ context.Context, s Snapshot, id string) (Runner, error) {
	return fakeRunner{f: f, s: s, id: id}, nil
}

func (r fakeRunner) Run(ctx context.Context, text string) (sdk.Result, error) {
	if r.f.started != nil {
		r.f.startOnce.Do(func() { close(r.f.started) })
	}
	if r.f.block != nil {
		select {
		case <-r.f.block:
		case <-ctx.Done():
			return sdk.Result{}, ctx.Err()
		}
	}
	r.f.mu.Lock()
	r.f.seen = append(r.f.seen, string(r.s.Profile.Kind)+"/"+r.s.Definition.Body+"/"+r.id)
	r.f.mu.Unlock()
	return sdk.Result{RunID: "run-1", Status: sdk.Succeeded, Text: text}, nil
}

func definition(mode string) string {
	return fmt.Sprintf("---\nschema_version: '2'\ndefinition_id: def:example\nrevision: '1'\nname: example\ndescription: Example agent.\nbehavior:\n  purpose: Reply to a turn.\nrequirements: {}\nharness_profile:\n  permissions:\n    profile: example\ncontinuity:\n  mode: %s\n---\nOriginal instructions.\n", mode)
}

func writeAgent(t *testing.T, root, name, mode, profile string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(definition(mode)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestKindsLifetimeReloadAndHTTP(t *testing.T) {
	root := t.TempDir()
	stable := writeAgent(t, root, "stable", "durable", "kind: stable\nlifetime: on-demand\nseat_id: seat-1\n")
	writeAgent(t, root, "task", "durable", "kind: task\nlifetime: until-task-end\ntask_id: task-1\n")
	writeAgent(t, root, "embedded", "ephemeral", "kind: embedded\nlifetime: application\napplication_id: app-1\n")
	store := NewMemoryStore()
	factory := &fakeFactory{}
	h := Host{Root: root, Store: store, Factory: factory}
	ctx := context.Background()
	if _, err := h.Turn(ctx, "stable", "one"); err != nil {
		t.Fatal(err)
	}
	// A new Host over the same store represents an in-process host restart.
	if _, err := (Host{Root: root, Store: store, Factory: factory}).Turn(ctx, "stable", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Turn(ctx, "task", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Turn(ctx, "embedded", "one"); err != nil {
		t.Fatal(err)
	}
	if err := h.EndTask(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Turn(ctx, "task", "two"); !errors.Is(err, ErrTaskEnded) {
		t.Fatalf("ended task input: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stable, "agent.md"), []byte(strings.Replace(definition("durable"), "Original", "Updated", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Turn(ctx, "stable", "three"); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	seen := append([]string(nil), factory.seen...)
	factory.mu.Unlock()
	if len(seen) != 5 || !strings.Contains(seen[0], "Original") || !strings.Contains(seen[1], "Original") || !strings.Contains(seen[4], "Updated") {
		t.Fatalf("reload: %v", seen)
	}
	if seen[0][strings.LastIndex(seen[0], "/"):] != seen[1][strings.LastIndex(seen[1], "/"):] {
		t.Fatalf("stable conversation changed on restart: %v", seen[:2])
	}
	req := httptest.NewRequest(http.MethodPost, "/agents/stable/turns", strings.NewReader(`{"text":"hello"}`))
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `event: run.finish`) || !strings.Contains(w.Body.String(), `event: message.start`) || !strings.Contains(w.Body.String(), `"text":"hello"`) {
		t.Fatalf("HTTP chatstream: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/agents/task/turns", strings.NewReader(`{"text":"late"}`)))
	if w.Code != http.StatusGone {
		t.Fatalf("ended task HTTP status: %d", w.Code)
	}
}

func TestProfileValidationAndMalformedFiles(t *testing.T) {
	root := t.TempDir()
	dir := writeAgent(t, root, "bad", "ephemeral", "kind: stable\nlifetime: on-demand\nseat_id: seat-1\n")
	if _, err := Load(dir); !errors.Is(err, ErrProfile) {
		t.Fatalf("unsupported continuity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("kind: concurrent\nlifetime: on-demand\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrProfile) {
		t.Fatalf("unsupported kind: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("kind: embedded\nlifetime: application\napplication_id: app\nunknown: value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrProfile) {
		t.Fatalf("unknown field: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("kind: embedded\nlifetime: application\napplication_id: app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted trailing MCP document")
	}
	if err := os.Remove(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted symlinked skills directory")
	}
	if err := os.Remove(filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte("not a definition"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted malformed agent.md")
	}
}

func TestExampleDirectories(t *testing.T) {
	root := filepath.Join("examples", "agents")
	for _, name := range []string{"stable", "task", "embedded"} {
		snap, err := Load(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(snap.Profile.Kind) != name {
			t.Fatalf("%s: wrong kind %s", name, snap.Profile.Kind)
		}
	}
	f := &fakeFactory{}
	h := Host{Root: root, Store: NewMemoryStore(), Factory: f}
	for _, name := range []string{"stable", "task", "embedded"} {
		if _, err := h.Turn(context.Background(), name, "hello"); err != nil {
			t.Fatalf("%s boot: %v", name, err)
		}
	}
	for _, name := range []string{"stable", "task"} {
		req := httptest.NewRequest(http.MethodPost, "/agents/"+name+"/turns", strings.NewReader(`{"text":"from HTTP"}`))
		w := httptest.NewRecorder()
		h.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(w.Body.String(), "event: run.finish") {
			t.Fatalf("%s chatstream: %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestEmbeddedDirectoryRunsActualSDKLoop(t *testing.T) {
	root := t.TempDir()
	dir := writeAgent(t, root, "embedded", "ephemeral", "kind: embedded\nlifetime: application\napplication_id: app-1\n")
	def := strings.Replace(definition("ephemeral"), "requirements: {}", "requirements:\n  tools: [read_value]", 1)
	def = strings.Replace(def, "profile: example", "profile: synthetic", 1)
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte(def), 0600); err != nil {
		t.Fatal(err)
	}
	fh := fakehost.New()
	factory := SDKFactory{
		Ports: func(_ context.Context, _ Snapshot, conversation string) (sdk.Ports, error) {
			fh.Binding.ConversationID = runtimecontract.ConversationID(conversation)
			return fh.Ports(), nil
		},
		Input: func(_ context.Context, _ Snapshot, _ string, text string) (sdk.Input, error) {
			in := fh.Input()
			in.Text = text
			return in, nil
		},
	}
	h := Host{Root: root, Store: NewMemoryStore(), Factory: factory}
	result, err := h.Turn(context.Background(), "embedded", "Read the synthetic value.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != sdk.Succeeded || result.Text != "The value is 42." || fh.Effects != 1 || fh.ModelCalls != 2 {
		t.Fatalf("actual SDK loop: %+v effects=%d models=%d", result, fh.Effects, fh.ModelCalls)
	}
}

func TestEndTaskCancelsActiveTurnAndBusy(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "task", "durable", "kind: task\nlifetime: until-task-end\ntask_id: task-1\n")
	f := &fakeFactory{block: make(chan struct{}), started: make(chan struct{})}
	h := Host{Root: root, Store: NewMemoryStore(), Factory: f}
	finished := make(chan error, 1)
	go func() { _, err := h.Turn(context.Background(), "task", "first"); finished <- err }()
	<-f.started
	if _, err := h.Turn(context.Background(), "task", "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second writer: %v", err)
	}
	if err := h.EndTask(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("active task cancellation: %v", err)
	}
}
