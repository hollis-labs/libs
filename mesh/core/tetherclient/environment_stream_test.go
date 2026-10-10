package tether

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnvironmentGapExplicitBeforeSnapshotAndResubscribe(t *testing.T) {
	var streams, snapshots atomic.Int32
	srv := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sessions/session-test/snapshot" {
			snapshots.Add(1)
			_, _ = fmt.Fprint(w, `{"environment_id":"env-test","high_water_seq":10,"earliest_available":5,"sessions":[{"id":"session-test","pending_known":false}]}`)
			return
		}
		if r.URL.Path != "/sessions/session-test/stream" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if streams.Add(1) == 1 {
			if r.URL.Query().Get("after_seq") != "1" {
				t.Error("wrong initial cursor")
			}
			_, _ = fmt.Fprint(w, "id: 1\nevent: gap\ndata: {\"environment_id\":\"env-test\",\"reason\":\"purged\",\"earliest_available\":5,\"high_water_seq\":10,\"snapshot_required\":true}\n\n")
			return
		}
		if r.URL.Query().Get("after_seq") != "10" {
			t.Error("gap advanced without snapshot")
		}
		writeEnvironmentTestEvent(w, 11, "session-test")
		writeEnvironmentTestSync(w, 12)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	one := int64(1)
	s, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{}).Subscribe(context.Background(), EnvironmentStreamOptions{SessionID: "session-test", AfterSeq: &one})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	gap := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.Gap != nil || u.Snapshot != nil })
	if gap.Gap == nil || gap.State.AfterSeq != 1 || gap.State.Freshness != "gap" {
		t.Fatal("snapshot occurred before explicit gap delivery")
	}
	snapshot := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.Snapshot != nil || u.Event != nil })
	if snapshot.Snapshot == nil || snapshot.Snapshot.HighWaterSeq != 10 || snapshot.Snapshot.Sessions[0].PendingKnown {
		t.Fatal("snapshot projection lost")
	}
	event := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.Event != nil })
	if event.Event.Seq != 11 || event.Event.Kind != "future.kind" {
		t.Fatal("event after snapshot lost")
	}
	fresh := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Freshness == "fresh" })
	if fresh.State.AfterSeq != 12 {
		t.Fatal("filtered global cursor not advanced")
	}
	if snapshots.Load() != 1 {
		t.Fatal("gap did not obtain one replacement snapshot")
	}
}

func TestEnvironmentInitialSnapshotAndLongLivedContext(t *testing.T) {
	release := make(chan struct{})
	srv := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/environment/snapshot" {
			_, _ = fmt.Fprint(w, `{"environment_id":"env-test","high_water_seq":7,"earliest_available":1,"sessions":[]}`)
			return
		}
		if r.URL.Query().Get("after_seq") != "7" {
			t.Error("initial snapshot cursor not used")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeEnvironmentTestEvent(w, 8, "")
		writeEnvironmentTestSync(w, 8)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	s, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{HTTPClient: &http.Client{Timeout: time.Millisecond}, ConnectTimeout: time.Second}).Subscribe(context.Background(), EnvironmentStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEnvironmentSubscription(t, s)
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.Snapshot != nil })
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "connected" })
	// Stream data is deliberately held beyond the caller HTTP client's timeout.
	time.Sleep(10 * time.Millisecond)
	close(release)
	update := nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.Event != nil })
	if update.Event.Seq != 8 {
		t.Fatal("long-lived stream did not survive transport timeout")
	}
}

func TestEnvironmentParserDedupUnknownAndTruncation(t *testing.T) {
	var wire strings.Builder
	for _, seq := range []int{2, 2, 1, 3} {
		_, _ = fmt.Fprintf(&wire, "id: %d\nevent: future.kind\ndata: {\"environment_id\":\"env-test\",\"seq\":%d,\"id\":\"event-%d\",\"kind\":\"future.kind\",\"payload\":{\"unknown\":true},\"future_field\":123}\n\n", seq, seq, seq)
	}
	wire.WriteString("id: 4\nevent: future.kind\ndata: {\"environment_id\":\"env-test\",\"seq\":4,\"id\":\"event-4\",\"kind\":\"future.kind\"}")
	var seqs []int64
	err := parseEnvironmentStream(context.Background(), strings.NewReader(wire.String()), "env-test", "", 1, func(frame environmentFrame) error {
		seqs = append(seqs, frame.event.Seq)
		if string(frame.event.Payload) != `{"unknown":true}` {
			t.Fatal("unknown payload lost")
		}
		return nil
	})
	if !errors.Is(err, io.EOF) || fmt.Sprint(seqs) != "[2 3]" {
		t.Fatalf("seqs=%v err=%v", seqs, err)
	}
}

func TestEnvironmentParserRefusesWrongCoordinates(t *testing.T) {
	for _, data := range []string{
		`{"environment_id":"other","seq":1,"id":"e","kind":"k"}`,
		`{"environment_id":"env-test","seq":2,"id":"e","kind":"k"}`,
		`{"environment_id":"env-test","seq":1,"id":"e","kind":"k","session_id":"other"}`,
	} {
		wire := "id: 1\nevent: k\ndata: " + data + "\n\n"
		err := parseEnvironmentStream(context.Background(), strings.NewReader(wire), "env-test", "session-test", 0, func(environmentFrame) error { t.Fatal("invalid event delivered"); return nil })
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatal("invalid coordinates accepted")
		}
	}
}

func TestEnvironmentGapControlNamesRemainValidEventKinds(t *testing.T) {
	for _, kind := range []string{"gap", "synchronized"} {
		wire := fmt.Sprintf("id: 1\nevent: %s\ndata: {\"environment_id\":\"env-test\",\"seq\":1,\"id\":\"e\",\"kind\":%q,\"payload\":null}\n\n", kind, kind)
		var event *EnvironmentEvent
		err := parseEnvironmentStream(context.Background(), strings.NewReader(wire), "env-test", "", 0, func(f environmentFrame) error { event = f.event; return nil })
		if !errors.Is(err, io.EOF) || event == nil || event.Kind != kind {
			t.Fatal("durable event collided with stream control")
		}
	}
}

func TestEnvironmentSnapshotWrongIdentityAndSession(t *testing.T) {
	for _, snapshot := range []string{
		`{"environment_id":"other","high_water_seq":1}`,
		`{"environment_id":"env-test","high_water_seq":-1}`,
		`{"environment_id":"env-test","earliest_available":0}`,
		`{"environment_id":"env-test","high_water_seq":1,"sessions":[{"id":"other"}]}`,
	} {
		srv := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, snapshot) })
		conn, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{}).Connect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Snapshot(context.Background(), "session-test")
		if err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}

func TestEnvironmentCancellationUnderBackpressure(t *testing.T) {
	srv := testEnvironmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; i <= 100; i++ {
			writeEnvironmentTestEvent(w, i, "")
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	zero := int64(0)
	s, err := testEnvironment(t, []string{srv.URL}, EnvironmentOptions{}).Subscribe(context.Background(), EnvironmentStreamOptions{AfterSeq: &zero})
	if err != nil {
		t.Fatal(err)
	}
	nextEnvironmentUpdate(t, s, func(u EnvironmentUpdate) bool { return u.State.Transport == "connected" })
	// Stop with an established reader while the consumer isn't reading events.
	closeEnvironmentSubscription(t, s)
	if err := <-s.Errors; !errors.Is(err, context.Canceled) {
		t.Fatalf("terminal outcome %v", err)
	}
}

func TestEnvironmentForwardWireFields(t *testing.T) {
	var snapshot EnvironmentSnapshot
	err := json.Unmarshal([]byte(`{"environment_id":"env-test","high_water_seq":4,"earliest_available":1,"sessions":[{"id":"s","instance_id":"i","logical_agent_id":"a","kind":"claude","route":{"provider":"claude"},"session_state":"active","instance_status":"running","instance_detail":{},"last_activity":"2026-01-01T00:00:00Z","pending_question":true,"pending_question_known":true,"pending_approval":false,"pending_approval_known":true,"pending_known":true}]}`), &snapshot)
	if err != nil || !snapshot.Sessions[0].PendingKnown || !snapshot.Sessions[0].QuestionKnown || !snapshot.Sessions[0].ApprovalKnown || snapshot.Sessions[0].LogicalAgentID != "a" {
		t.Fatalf("wire mirror failed: %v", err)
	}
}
