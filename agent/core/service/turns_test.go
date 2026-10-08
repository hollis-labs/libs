package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	streamhub "github.com/hollis-labs/go-streamhub"
	"github.com/hollis-labs/substrate/agent/service"
)

type output struct {
	Text string `json:"text"`
}
type snapshots struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *snapshots) GetCognitiveTurnSnapshot(_ context.Context, view, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.data[view+"/"+id]; ok {
		return data, nil
	}
	return "", sql.ErrNoRows
}
func (s *snapshots) SaveCognitiveTurnSnapshot(_ context.Context, view, id, data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[view+"/"+id] = data
	return nil
}

func TestHostCommitAndCanonicalSnapshotSurviveReload(t *testing.T) {
	backing := &snapshots{data: make(map[string]string)}
	turns := service.NewTurns[output](backing, service.Options{})
	body := &output{Text: "committed answer"}
	run := turns.Create("view", "turn", "fake", "model", "live", "normal", func() (service.Committed[output], error) {
		return service.Committed[output]{Message: body, Content: &body.Text}, nil
	})
	turns.Working("turn")
	run.Consume(service.Input{Type: "delta", Content: "streamed"})
	run.Consume(service.Input{Type: "stream_end"})
	before, err := turns.Get("view", "turn")
	if err != nil || before.State != "completed" || before.Content != body.Text || before.Message.LastSeq != before.EventCheckpoint {
		t.Fatalf("snapshot=%+v err=%v", before, err)
	}
	reloaded := service.NewTurns[output](backing, service.Options{})
	after, err := reloaded.Get("view", "turn")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("reload=%+v err=%v", after, err)
	}
	if _, err = reloaded.Subscribe(t.Context(), "view", "turn", 0); !errors.Is(err, streamhub.ErrUnknownStream) {
		t.Fatalf("log falsely survived restart: %v", err)
	}
	if _, err = reloaded.Get("other", "turn"); !errors.Is(err, service.ErrTurnNotFound) {
		t.Fatalf("foreign owner accepted: %v", err)
	}
}

func TestReloadUnfinishedRunRecordsStableFailure(t *testing.T) {
	backing := &snapshots{data: make(map[string]string)}
	turns := service.NewTurns[output](backing, service.Options{})
	turns.Create("view", "unfinished", "fake", "model", "live", "normal", nil)
	reloaded := service.NewTurns[output](backing, service.Options{})
	lost, err := reloaded.Get("view", "unfinished")
	if err != nil || lost.State != "failed" || lost.Message.Error.Code != "process_lost" {
		t.Fatalf("snapshot=%+v err=%v", lost, err)
	}
	again, err := service.NewTurns[output](backing, service.Options{}).Get("view", "unfinished")
	if err != nil || !reflect.DeepEqual(lost, again) {
		t.Fatalf("outcome changed on second reload: %+v err=%v", again, err)
	}
}

func TestMissingHostCommitCannotYieldSuccessfulTerminal(t *testing.T) {
	turns := service.NewTurns[output](nil, service.Options{})
	run := turns.Create("view", "turn", "fake", "model", "live", "normal", func() (service.Committed[output], error) {
		return service.Committed[output]{}, errors.New("host commit failed")
	})
	run.Consume(service.Input{Type: "delta", Content: "partial"})
	run.Consume(service.Input{Type: "stream_end"})
	run.Consume(service.Input{Type: "delta", Content: "after terminal"})
	run.End()
	snapshot, err := turns.Get("view", "turn")
	if err != nil || snapshot.State != "failed" || snapshot.Message.Error.Code != "persistence_failed" || snapshot.Content != "partial" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil || !json.Valid(data) {
		t.Fatalf("invalid snapshot serialization: %v", err)
	}
}

type failingSnapshots struct {
	snapshots
	failure error
}

func (s *failingSnapshots) SaveCognitiveTurnSnapshot(ctx context.Context, view, id, data string) error {
	if s.failure != nil {
		return s.failure
	}
	return s.snapshots.SaveCognitiveTurnSnapshot(ctx, view, id, data)
}

func TestPersistentSnapshotOutageIsObservableWithoutDurableClaim(t *testing.T) {
	for _, duringCreate := range []bool{false, true} {
		t.Run(map[bool]string{false: "consume", true: "create"}[duringCreate], func(t *testing.T) {
			cause := errors.New("snapshot store unavailable")
			backing := &failingSnapshots{snapshots: snapshots{data: make(map[string]string)}}
			if duringCreate {
				backing.failure = cause
			}
			turns := service.NewTurns[output](backing, service.Options{})
			run := turns.Create("view", "turn", "fake", "model", "live", "normal", nil)
			if !duringCreate {
				backing.failure = cause
			}
			err := run.Consume(service.Input{Type: "delta", Content: "partial"})
			if !errors.Is(err, service.ErrSnapshotPersistence) || !errors.Is(err, cause) || !errors.Is(run.PersistenceError(), cause) || !errors.Is(run.End(), cause) {
				t.Fatalf("store failure hidden: %v", err)
			}
			live, err := turns.Get("view", "turn")
			if err != nil || live.State != "failed" || live.Message.Error.Code != "persistence_failed" {
				t.Fatalf("false successful outcome: %+v %v", live, err)
			}
			backing.failure = nil
			fresh := service.NewTurns[output](backing, service.Options{})
			recovered, err := fresh.Get("view", "turn")
			if duringCreate {
				if !errors.Is(err, service.ErrTurnNotFound) {
					t.Fatalf("uncommitted creation falsely recovered: %+v %v", recovered, err)
				}
			} else if err != nil || recovered.Message.Error.Code != "process_lost" || recovered.EventCheckpoint >= live.EventCheckpoint {
				t.Fatalf("failed terminal falsely durable: %+v %v", recovered, err)
			}
		})
	}
}
