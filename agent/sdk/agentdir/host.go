package agentdir

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/libs/agent/sdk"
)

var (
	ErrTaskEnded = errors.New("agentdir: task has ended")
	ErrBusy      = errors.New("agentdir: conversation already has an input writer")
)

// Lease is an authoritative, single-writer conversation admission. The store
// must retain the conversation for stable and task kinds across host restarts.
// Release does not delete the conversation.
type Lease interface {
	ConversationID() string
	Context() context.Context
	Release()
}

// Store owns conversation identity, task terminal state and writer arbitration.
// Acquire must refuse ended tasks and concurrent writers atomically. It must
// never select another agent's conversation by recency.
type Store interface {
	Acquire(context.Context, Snapshot) (Lease, error)
	EndTask(context.Context, string) error
}

// Runner is the same headless API used by embedded and HTTP hosts. A production
// factory adapts sdk.Operative.Run with host-issued binding and environment.
type Runner interface {
	Run(context.Context, string) (sdk.Result, error)
}

// StreamingRunner reports model text while the run is active. The callback
// returns an error on client disconnect, which must stop the producer.
type StreamingRunner interface {
	RunStream(context.Context, string, func(string) error) (sdk.Result, error)
}

var ErrStreamingUnavailable = errors.New("agentdir: runner does not stream")

type Factory interface {
	New(context.Context, Snapshot, string) (Runner, error)
}

// Host never caches authored definitions. Each turn reloads agent.md and
// host.yaml; the store is a projection of conversation state only.
type Host struct {
	Root    string
	Store   Store
	Factory Factory
}

func (h Host) Turn(ctx context.Context, agent, text string) (sdk.Result, error) {
	runner, lease, err := h.prepare(ctx, agent, text)
	if err != nil {
		return sdk.Result{}, err
	}
	defer lease.Release()
	result, err := runner.Run(lease.Context(), text)
	if err != nil {
		return result, err
	}
	if writer, ok := lease.(interface {
		Commit(string, sdk.Result) error
	}); ok {
		if err := writer.Commit(text, result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (h Host) TurnStream(ctx context.Context, agent, text string, delta func(string) error) (sdk.Result, error) {
	if delta == nil {
		return sdk.Result{}, fmt.Errorf("agentdir: delta sink required")
	}
	runner, lease, err := h.prepare(ctx, agent, text)
	if err != nil {
		return sdk.Result{}, err
	}
	defer lease.Release()
	streaming, ok := runner.(StreamingRunner)
	if !ok {
		return sdk.Result{}, ErrStreamingUnavailable
	}
	result, err := streaming.RunStream(lease.Context(), text, delta)
	if err != nil {
		return result, err
	}
	if writer, ok := lease.(interface {
		Commit(string, sdk.Result) error
	}); ok {
		if err := writer.Commit(text, result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (h Host) prepare(ctx context.Context, agent, text string) (Runner, Lease, error) {
	if !validAgentName(agent) || strings.TrimSpace(text) == "" || h.Store == nil || h.Factory == nil {
		return nil, nil, fmt.Errorf("agentdir: agent, text, store and factory required")
	}
	snap, err := Load(filepath.Join(h.Root, agent))
	if err != nil {
		return nil, nil, err
	}
	lease, err := h.Store.Acquire(ctx, snap)
	if err != nil {
		return nil, nil, err
	}
	runner, err := h.Factory.New(lease.Context(), snap, lease.ConversationID())
	if err != nil {
		lease.Release()
		return nil, nil, err
	}
	return runner, lease, nil
}

func (h Host) EndTask(ctx context.Context, taskID string) error {
	if strings.TrimSpace(taskID) == "" || h.Store == nil {
		return fmt.Errorf("agentdir: task ID and store required")
	}
	return h.Store.EndTask(ctx, taskID)
}

func validAgentName(s string) bool {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\\`) {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Handler provides a minimal standalone surface. POST /agents/{name}/turns
// accepts {"text":"..."} and emits chatstream/v1 native SSE. Hosts may expose
// the same Host.Turn through their own authenticated transport instead.
func (h Host) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "agents" || parts[2] != "turns" || !validAgentName(parts[1]) {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Text string `json:"text"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
			http.Error(w, "invalid turn", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			http.Error(w, "invalid turn", http.StatusBadRequest)
			return
		}
		runID := "stream_" + rand.Text()
		seq := 0
		started := false
		emit := func(verb string, extra map[string]any) error {
			seq++
			event := map[string]any{"v": "1", "seq": seq, "run_id": runID, "time": time.Now().UTC().Format(time.RFC3339Nano), "verb": verb}
			for k, v := range extra {
				event[k] = v
			}
			data, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, verb, data); err != nil {
				return err
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return nil
		}
		start := func() error {
			if started {
				return nil
			}
			started = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			if err := emit("run.start", nil); err != nil {
				return err
			}
			if err := emit("message.start", map[string]any{"message_id": "answer", "role": "assistant"}); err != nil {
				return err
			}
			return emit("part.start", map[string]any{"part_id": "answer", "kind": "text"})
		}
		result, err := h.TurnStream(r.Context(), parts[1], body.Text, func(text string) error {
			if err := start(); err != nil {
				return err
			}
			return emit("part.delta", map[string]any{"part_id": "answer", "text": text})
		})
		if err != nil {
			if started {
				_ = emit("run.error", map[string]any{"code": "agent_run_failed", "message": err.Error()})
				return
			}
			code := http.StatusUnprocessableEntity
			if errors.Is(err, ErrTaskEnded) {
				code = http.StatusGone
			}
			if errors.Is(err, ErrBusy) {
				code = http.StatusConflict
			}
			http.Error(w, err.Error(), code)
			return
		}
		if err := start(); err != nil {
			return
		}
		if err := emit("part.end", map[string]any{"part_id": "answer"}); err != nil {
			return
		}
		if err := emit("message.end", nil); err != nil {
			return
		}
		if result.Status == sdk.Succeeded {
			_ = emit("run.finish", map[string]any{"reason": "stop"})
		} else {
			_ = emit("run.error", map[string]any{"code": "agent_run_failed", "message": string(result.Status)})
		}
	})
}
