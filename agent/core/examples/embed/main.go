// Command embed exercises the public core with fake provider, tool and policy
// ports. It opens only the explicitly selected local example database.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
	"github.com/hollis-labs/libs/ui-go/chatstream/hubbind"
	"github.com/hollis-labs/substrate/agent/approval"
	"github.com/hollis-labs/substrate/agent/runloop"
	agentservice "github.com/hollis-labs/substrate/agent/service"
	"github.com/hollis-labs/substrate/agent/subagent"
	"github.com/hollis-labs/substrate/agent/tooluse"
	"github.com/hollis-labs/substrate/agent/transport/httpstream"
	"github.com/hollis-labs/substrate/agent/turn"
	permission "github.com/hollis-labs/substrate/harness/interception/permission"
	llmtypes "github.com/hollis-labs/substrate/llm-core/llmtypes"
	messaging "github.com/hollis-labs/substrate/mesh/messaging/mailbox"

	_ "modernc.org/sqlite"
)

type output struct {
	Text        string `json:"text"`
	Interrupted bool   `json:"interrupted"`
}

// The embedding host owns this storage contract, output commit and policy.
// No application migrations or application package imports are required.
type hostStore struct{ db *sql.DB }

func (s hostStore) GetCognitiveTurnSnapshot(ctx context.Context, view, id string) (string, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM example_snapshots WHERE view_id=? AND run_id=?", view, id).Scan(&data)
	return data, err
}
func (s hostStore) SaveCognitiveTurnSnapshot(ctx context.Context, view, id, data string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO example_snapshots(view_id,run_id,data) VALUES(?,?,?) ON CONFLICT(view_id,run_id) DO UPDATE SET data=excluded.data", view, id, data)
	return err
}
func (s hostStore) commit(id string, value output) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO example_outputs(run_id,data) VALUES(?,?) ON CONFLICT(run_id) DO UPDATE SET data=excluded.data", id, string(data))
	return err
}
func (s hostStore) load(id string) (agentservice.Committed[output], error) {
	var value output
	var data string
	if err := s.db.QueryRow("SELECT data FROM example_outputs WHERE run_id=?", id).Scan(&data); err != nil {
		return agentservice.Committed[output]{}, err
	}
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return agentservice.Committed[output]{}, err
	}
	return agentservice.Committed[output]{Message: &value, Content: &value.Text, Interrupted: value.Interrupted}, nil
}

func fakeEvents(tool bool, truncated bool) <-chan llmtypes.StreamEvent {
	events := make(chan llmtypes.StreamEvent, 4)
	text, stop := "answer from fake provider", "end_turn"
	if tool {
		text, stop = "looking up the example", "tool_use"
	}
	events <- llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: text}
	if tool {
		events <- llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &llmtypes.ToolUseBlock{ID: "lookup-call", Name: "lookup", Input: map[string]any{"key": "example"}}}
	}
	events <- llmtypes.StreamEvent{Type: llmtypes.EventUsage, Usage: &llmtypes.Usage{InputTokens: 1, OutputTokens: 1, StopReason: stop}}
	if !truncated {
		events <- llmtypes.StreamEvent{Type: llmtypes.EventDone}
	}
	close(events)
	return events
}

func runExampleCase(backing hostStore, mode string) (agentservice.Snapshot[output], error) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	turns := agentservice.NewTurns[output](backing, agentservice.Options{})
	run := turns.Create("example-view", mode, "fake", "fake-model", "live", "normal", func() (agentservice.Committed[output], error) { return backing.load(mode) })
	if err := run.PersistenceError(); err != nil {
		return agentservice.Snapshot[output]{}, err
	}
	turns.Working(mode)
	engine := permission.NewEngine(permission.ModeDefault, &permission.RuleSet{Rules: []permission.Rule{{Tool: "lookup", Behavior: permission.DecisionAsk}}})
	approvals := approval.New(engine)
	attempts, tools, finalized := 0, 0, 0
	iteration := 0
	var indices []int
	var hostErr error
	if mode == "canceled" {
		cancel(errors.New("embedding caller canceled its turn"))
	}
	_, loopErr := runloop.Execute(ctx, runloop.Ports[<-chan llmtypes.StreamEvent, turn.TurnResult]{
		Iteration: func(i int) { iteration = i; indices = append(indices, i) },
		Request: func(ctx context.Context) (<-chan llmtypes.StreamEvent, runloop.Directive) {
			attempts++
			if ctx.Err() != nil {
				turns.Ending(mode, true)
				hostErr = backing.commit(mode, output{Text: "accepted turn canceled", Interrupted: true})
				return nil, runloop.Terminate
			}
			if attempts == 1 {
				return nil, runloop.Retry
			}
			return fakeEvents(mode == "completed" && iteration == 0, mode == "truncated"), runloop.Proceed
		},
		Consume: func(ctx context.Context, events <-chan llmtypes.StreamEvent) (turn.TurnResult, runloop.Directive) {
			phase := "final"
			if mode == "completed" && iteration == 0 {
				phase = "narration"
			}
			result, err := turn.ExecuteTurn(ctx, turn.TurnRequest{Stream: func(context.Context) (<-chan llmtypes.StreamEvent, error) { return events, nil }, IdleTimeout: time.Second, RequireTerminal: true, Sink: turn.TurnSink{OnDelta: func(text string) { run.Consume(agentservice.Input{Type: "delta", Phase: phase, Content: text}) }}})
			if err != nil {
				code := "provider_error"
				if errors.Is(err, turn.ErrTurnTruncated) {
					code = chatstream.CodeUpstreamTruncated
				}
				run.Consume(agentservice.Input{Type: "error", Failure: &chatstream.RunError{Code: code, Message: err.Error()}})
				hostErr = backing.commit(mode, output{Text: result.Text})
				return result, runloop.Terminate
			}
			if len(result.ToolUseBlocks) == 0 {
				return result, runloop.Finish
			}
			return result, runloop.Proceed
		},
		Settle: func(ctx context.Context, result turn.TurnResult) runloop.Directive {
			call := result.ToolUseBlocks[0]
			prompt := approvals.Request("example-view", mode, call.ID, call.Name, call.Input, "fake host requires approval")
			data, _ := json.Marshal(agentservice.ApprovalPrompt{RequestID: prompt.ID, CallID: call.ID, Tool: call.Name, Input: call.Input, Reason: prompt.Reason, ExpiresAt: prompt.CreatedAt.Add(permission.DefaultApprovalTimeout)})
			run.Consume(agentservice.Input{Type: "approval_request", Data: string(data)})
			if _, err := approvals.Respond(ctx, "example-view", prompt.ID, permission.DecisionAllow, permission.ScopeSession); !errors.Is(err, approval.ErrScope) {
				hostErr = fmt.Errorf("broader scope accepted: %v", err)
				return runloop.Terminate
			}
			if err := approvals.RespondRetained(ctx, "example-view", prompt.ID, permission.DecisionAllow, permission.ScopeOnce); err != nil {
				hostErr = err
				return runloop.Terminate
			}
			decision, err := approvals.Respond(ctx, "example-view", prompt.ID, permission.DecisionAllow, permission.ScopeOnce)
			if err != nil || decision.RunID != mode || decision.CallID != call.ID {
				hostErr = fmt.Errorf("lost run/call binding: %+v %v", decision, err)
				return runloop.Terminate
			}
			engine.WaitForApproval(ctx, prompt)
			approvals.Finish(prompt.ID)
			if engine.Check(ctx, "example-view", call.Name, call.Input, permission.ToolMeta{}).Decision != permission.DecisionAsk {
				hostErr = errors.New("once approval granted a later call")
				return runloop.Terminate
			}
			tooluse.Batch(ctx, []tooluse.Job{{Ready: true}}, 1, func(int, bool) {
				run.Consume(agentservice.Input{Type: "tool_call", Tool: call.Name, ToolID: call.ID})
				tools++
				run.Consume(agentservice.Input{Type: "tool_result", Tool: call.Name, ToolID: call.ID, Summary: "fake lookup result"})
			}, func(int) { hostErr = errors.New("approved call was skipped") }, nil)
			return runloop.Continue
		},
		Finalize: func(context.Context) runloop.Directive {
			finalized++
			hostErr = backing.commit(mode, output{Text: "answer from fake provider"})
			run.Consume(agentservice.Input{Type: "stream_end"})
			return runloop.Proceed
		},
	})
	if err := run.End(); err != nil {
		return agentservice.Snapshot[output]{}, err
	}
	if loopErr != nil {
		return agentservice.Snapshot[output]{}, loopErr
	}
	if hostErr != nil {
		return agentservice.Snapshot[output]{}, hostErr
	}
	snapshot, err := turns.Get("example-view", mode)
	if err != nil {
		return snapshot, err
	}
	if mode == "completed" && (tools != 1 || finalized != 1 || !reflect.DeepEqual(indices, []int{0, 0, 1})) {
		return snapshot, fmt.Errorf("loop ownership mismatch: tools=%d finalized=%d indices=%v", tools, finalized, indices)
	}
	if mode != "completed" && finalized != 0 {
		return snapshot, errors.New("interrupted turn finalized successfully")
	}
	sub, err := turns.Subscribe(context.WithoutCancel(ctx), "example-view", mode, 0)
	if err != nil {
		return snapshot, err
	}
	var events []chatstream.Event
	for {
		item, err := sub.Next(context.Background())
		if err != nil {
			break
		}
		event, err := hubbind.Decode(item.Record)
		if err != nil {
			return snapshot, err
		}
		events = append(events, event)
		if event.IsTerminal() {
			break
		}
	}
	_ = sub.Close()
	if violations := conformance.Validate(events); len(violations) > 0 {
		return snapshot, fmt.Errorf("canonical violations: %v", violations)
	}
	if len(events) == 0 || !events[len(events)-1].IsTerminal() {
		return snapshot, errors.New("missing canonical terminal")
	}
	if _, err := turns.Get("foreign-view", mode); !errors.Is(err, agentservice.ErrTurnNotFound) {
		return snapshot, errors.New("foreign view read succeeded")
	}
	sub, err = turns.Subscribe(context.Background(), "example-view", mode, 0)
	if err != nil {
		return snapshot, err
	}
	w := httptest.NewRecorder()
	if err = httpstream.Write(w, httptest.NewRequest("GET", "/events", nil), sub, httpstream.State{RunID: mode, Checkpoint: snapshot.EventCheckpoint}, false); err != nil {
		return snapshot, err
	}
	if !strings.Contains(w.Body.String(), string(events[len(events)-1].Verb)) {
		return snapshot, errors.New("HTTP transport lost terminal")
	}
	return snapshot, nil
}

type childPolicy struct{}

func (childPolicy) AuthorizeSpawn(context.Context, string) (subagent.SpawnAuthorization, error) {
	return subagent.SpawnAuthorization{}, nil
}

type childMailbox struct{ sent int }

func (m *childMailbox) SendMessage(context.Context, messaging.SendInput) (*messaging.Message, error) {
	m.sent++
	return &messaging.Message{ID: "fake-child-reply"}, nil
}

func exercise(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS example_snapshots(view_id TEXT,run_id TEXT,data TEXT,PRIMARY KEY(view_id,run_id)); CREATE TABLE IF NOT EXISTS example_outputs(run_id TEXT PRIMARY KEY,data TEXT);` + subagent.SQLiteSchema); err != nil {
		return err
	}
	backing := hostStore{db}
	var before []agentservice.Snapshot[output]
	for _, mode := range []string{"completed", "canceled", "truncated"} {
		snapshot, err := runExampleCase(backing, mode)
		if err != nil {
			return fmt.Errorf("%s: %w", mode, err)
		}
		want := map[string]string{"completed": "completed", "canceled": "canceled", "truncated": "failed"}[mode]
		if snapshot.State != want || snapshot.Message.LastSeq != snapshot.EventCheckpoint {
			return fmt.Errorf("%s: unexpected persisted outcome %+v", mode, snapshot)
		}
		before = append(before, snapshot)
	}
	mailbox := &childMailbox{}
	children := subagent.NewService(db, subagent.EchoRunner{}, mailbox, nil, nil)
	if _, err := children.Spawn(context.Background(), subagent.SpawnRequest{ParentSessionID: "example-view", ParentAgentID: "fake-parent", Role: "fake-child", Prompt: "child work", Mode: subagent.ModeSync}); !errors.Is(err, subagent.ErrAuthorizationUnavailable) {
		return errors.New("child spawn accepted missing host authority")
	}
	children.SetSpawnAuthorizer(childPolicy{})
	reaper := children.NewReaper(subagent.ReaperOptions{})
	if _, err := reaper.SweepOnce(context.Background()); err != nil {
		return err
	}
	id, err := children.Spawn(context.Background(), subagent.SpawnRequest{ParentSessionID: "example-view", ParentAgentID: "fake-parent", Role: "fake-child", Prompt: "child work", Mode: subagent.ModeSync})
	if err != nil {
		return err
	}
	child, err := children.Status(context.Background(), id)
	if err != nil || child.Status != subagent.StatusCompleted || mailbox.sent != 1 {
		return fmt.Errorf("child lifecycle failed: %+v %v", child, err)
	}
	if err = db.Close(); err != nil {
		return err
	}
	reopened, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer reopened.Close()
	restored := agentservice.NewTurns[output](hostStore{reopened}, agentservice.Options{})
	for _, original := range before {
		snapshot, err := restored.Get(original.SessionViewID, original.TurnID)
		if err != nil || !reflect.DeepEqual(snapshot, original) {
			return fmt.Errorf("restart changed %s outcome: %v", original.TurnID, err)
		}
	}
	fmt.Println("PASS: retry identity, once-bound approval, tool settlement, child lifecycle, canceled/truncated terminals, canonical HTTP and persisted outcomes on database reopen")
	return nil
}

func main() {
	path := flag.String("db", "embed-example.db", "local example SQLite database")
	flag.Parse()
	if err := exercise(*path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
