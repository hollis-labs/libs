package shim

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

func rawController(t *testing.T, h *Host, spec Launch, minor int) (*net.UnixConn, Frame) {
	t.Helper()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(3 * time.Second))
	challenge, err := ReadFrame(c)
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Nonce string `json:"nonce"`
	}
	json.Unmarshal(challenge.Body, &b)
	err = WriteFrame(c, Frame{Major: 1, Minor: minor, Type: "hello", Session: spec.Session, Body: body(Hello{Major: 1, Minor: minor, Role: "controller", Proof: Proof(spec.Secret, b.Nonce, spec.Session, "controller"), Instance: spec.Instance, Generation: "1"})})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := ReadFrame(c)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Time{})
	return c, ready
}

func TestLargeBacklogSlowDrainAndTail(t *testing.T) {
	spec := launchTest(t, "echo")
	spec.ClientQueue = 64
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	awaitKind(t, h, "shim.output")
	for i := 0; i < 1200; i++ {
		if _, err := h.record("shim.marker", map[string]int{"index": i}, false); err != nil {
			t.Fatal(err)
		}
	}
	c := clientTest(t, h, spec, "controller")
	if err = c.Replay(spec.Session, ""); err != nil {
		t.Fatal(err)
	}
	receive(t, c, "result")
	// Let the bounded client channel fill before consuming the snapshot.
	time.Sleep(40 * time.Millisecond)
	for i := 0; i < 20; i++ {
		if _, err := h.record("shim.tail_marker", map[string]int{"index": i}, false); err != nil {
			t.Fatal(err)
		}
	}
	var last uint64
	markers, tail := 0, 0
	for tail < 20 {
		f := receive(t, c, "event")
		var b struct {
			Event mesh.Event `json:"event"`
		}
		json.Unmarshal(f.Body, &b)
		seq, err := h.journal.parseSafe(b.Event.Cursor)
		if err != nil || seq != last+1 {
			t.Fatalf("gap/duplicate: %d after %d (%v)", seq, last, err)
		}
		last = seq
		if b.Event.Kind == "shim.marker" {
			markers++
		}
		if b.Event.Kind == "shim.tail_marker" {
			tail++
		}
		if last%40 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if markers != 1200 {
		t.Fatalf("backlog incomplete: %d", markers)
	}
}

func TestAcknowledgmentsReachSteadyState(t *testing.T) {
	h, spec := hostTest(t, "echo")
	awaitKind(t, h, "shim.output")
	c := clientTest(t, h, spec, "controller")
	c.Replay(spec.Session, "")
	receive(t, c, "result")
	baseline := len(h.journal.Snapshot())
	events := 0
	quiet := time.NewTimer(100 * time.Millisecond)
	defer quiet.Stop()
	for {
		select {
		case f, ok := <-c.Frames:
			if !ok {
				t.Fatal("acking reader disconnected")
			}
			if f.Type == "event" {
				var b struct {
					Event mesh.Event `json:"event"`
				}
				json.Unmarshal(f.Body, &b)
				events++
				if events > baseline+10 {
					t.Fatal("ack feedback produces unbounded events")
				}
				if err := c.Ack(spec.Session, b.Event.Cursor); err != nil {
					t.Fatal(err)
				}
			}
		case <-quiet.C:
			if growth := len(h.journal.Snapshot()) - baseline; growth != 0 {
				t.Fatalf("ack grew journal by %d", growth)
			}
			return
		}
	}
}

func TestEscapedPipeDoesNotWedgeExit(t *testing.T) {
	spec := launchTest(t, "escape")
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure the escaped test process is killed even if the regression fails.
	t.Cleanup(func() {
		for _, e := range h.journal.Snapshot() {
			if e.Kind == "shim.output" {
				var b struct {
					Data string `json:"data"`
				}
				json.Unmarshal(e.Payload, &b)
				raw, _ := base64.StdEncoding.DecodeString(b.Data)
				var pid int
				if _, err := fmt.Sscanf(string(raw), "escaped:%d", &pid); err == nil {
					syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
		h.Close()
	})
	select {
	case <-h.Done():
	case <-time.After(700 * time.Millisecond):
		t.Fatal("escaped descendant wedged exit")
	}
	if got := h.Wait().Cause; got != "descendant_holds_pipe" {
		t.Fatal(got)
	}
	gap := awaitKind(t, h, "shim.output_gap")
	if !gap.Truncated {
		t.Fatal("missing truncated gap")
	}
}

func TestFinalHeaderCrashRecovery(t *testing.T) {
	for _, prefix := range [][]byte{nil, segmentHeader[:4]} {
		t.Run(fmt.Sprint(len(prefix)), func(t *testing.T) {
			spec := launchTest(t, "echo")
			j, err := OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20)
			if err != nil {
				t.Fatal(err)
			}
			id := j.identity.ID
			j.Close()
			os.WriteFile(filepath.Join(spec.JournalDir, "00000002.seg"), prefix, 0600)
			j, err = OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if j.identity.ID != id {
				t.Fatal("recovery changed identity")
			}
			entries, _ := os.ReadDir(spec.JournalDir)
			found := false
			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".torn-") {
					found = true
				}
			}
			if !found {
				t.Fatal("header evidence lost")
			}
		})
	}
}
func TestIdentityCrashBeforePublication(t *testing.T) {
	spec := launchTest(t, "echo")
	privateDir(spec.JournalDir)
	// A failed publication path leaves no live identity. The temporary name is
	// intentionally regular to exercise cleanup without symlinks.
	os.WriteFile(filepath.Join(spec.JournalDir, "identity.pending"), []byte("{\"id\":"), 0600)
	j, err := OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := os.Stat(filepath.Join(spec.JournalDir, "identity.pending")); !os.IsNotExist(err) {
		t.Fatal("torn identity staging file remains")
	}
	b, err := os.ReadFile(filepath.Join(spec.JournalDir, "identity.json"))
	if err != nil || !json.Valid(b) {
		t.Fatal("identity was not published atomically")
	}
}

func TestOversizedProvenanceDoesNotKillChild(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	req := injection(spec, "oversized", "")
	req.Subject = mesh.URN("urn:" + strings.Repeat("x", MaxFrame-2000))
	c.Send(spec.Session, "inject", req)
	f := receive(t, c, "error")
	var b struct {
		Code string `json:"code"`
	}
	json.Unmarshal(f.Body, &b)
	if b.Code != "invalid_request" {
		t.Fatal(b.Code)
	}
	select {
	case <-h.Done():
		t.Fatal("request validation killed healthy child")
	default:
	}
	c.Send(spec.Session, "inject", injection(spec, "valid", "still-live\n"))
	if resultCode(t, c) != "bytes_written" {
		t.Fatal("valid request refused after invalid input")
	}
}

func TestAckCannotBlockTakeoverOnSocketWrite(t *testing.T) {
	spec := launchTest(t, "echo")
	spec.Heartbeat = 200 * time.Millisecond
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	raw, ready := rawController(t, h, spec, 0)
	if ready.Type != "hello" {
		t.Fatal(ready.Type)
	}
	h.mu.Lock()
	controller := h.controller
	h.mu.Unlock()
	controller.socket.SetWriteBuffer(1024)
	raw.SetReadBuffer(1024)
	events := h.journal.Snapshot()
	e := events[len(events)-1]
	e.Payload = body(strings.Repeat("x", 400000))
	controller.enqueue(controller.frame("event", "", map[string]any{"event": e}))
	time.Sleep(20 * time.Millisecond)
	WriteFrame(raw, Frame{Major: 1, Type: "ack", Session: spec.Session, Epoch: controller.frame("", "", nil).Epoch, Body: body(map[string]string{"cursor": e.Cursor})})
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	next, err := Connect(h.SocketPath(), spec.Secret, spec.Session, spec.Instance, "1", "controller", true)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("takeover blocked behind write/ack: %v", elapsed)
	}
}

func TestConnectionAttachDetachEvidence(t *testing.T) {
	h, spec := hostTest(t, "echo")
	controller := clientTest(t, h, spec, "controller")
	observer := clientTest(t, h, spec, "observer")
	controller.Close()
	observer.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		seen := map[string]bool{}
		for _, e := range h.journal.Snapshot() {
			var b struct {
				Role string `json:"role"`
			}
			json.Unmarshal(e.Payload, &b)
			seen[e.Kind+":"+b.Role] = true
		}
		if seen["shim.attached:observer"] && seen["shim.detached:observer"] && seen["shim.detached:controller"] {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("missing observer attach or connection detach evidence")
}
func TestNegotiatedMinorAndErrorDetails(t *testing.T) {
	h, spec := hostTest(t, "echo")
	raw, ready := rawController(t, h, spec, 7)
	if ready.Type != "hello" {
		t.Fatal(ready.Type)
	}
	WriteFrame(raw, Frame{Major: 1, Minor: ready.Minor, Type: "unknown", RequestID: "unknown", Session: spec.Session, Epoch: ready.Epoch, Body: body(nil)})
	f, err := ReadFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	var b map[string]any
	json.Unmarshal(f.Body, &b)
	if f.Type != "error" || b["message"] == nil || b["retryable"] == nil {
		t.Fatal("missing command error details")
	}
	raw.Close()
	bad, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	ReadFrame(bad)
	WriteFrame(bad, Frame{Major: 9, Type: "hello", Session: spec.Session, Body: body(Hello{Major: 9})})
	f, err = ReadFrame(bad)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(f.Body, &b)
	if b["code"] != "unsupported_protocol" || b["supported_versions"] == nil || b["message"] == nil || b["retryable"] == nil {
		t.Fatalf("unsupported protocol details: %s", f.Body)
	}
}

func TestStoredReceiptDoesNotRepeatOutput(t *testing.T) {
	h, spec := hostTest(t, "echo")
	awaitKind(t, h, "shim.output")
	c := clientTest(t, h, spec, "controller")
	req := injection(spec, "once-real", "exactly-once\n")
	var receipts [2]receipt
	for i := range receipts {
		c.Send(spec.Session, "inject", req)
		json.Unmarshal(receive(t, c, "result").Body, &receipts[i])
	}
	if receipts[0] != receipts[1] {
		t.Fatal("retry did not return stored receipt")
	}
	h.stdin.Close()
	h.Wait()
	var output []byte
	for _, e := range h.journal.Snapshot() {
		if e.Kind == "shim.output" {
			var b struct {
				Data string `json:"data"`
			}
			json.Unmarshal(e.Payload, &b)
			raw, _ := base64.StdEncoding.DecodeString(b.Data)
			output = append(output, raw...)
		}
	}
	if count := bytes.Count(output, []byte("exactly-once\n")); count != 1 {
		t.Fatalf("effect executed %d times", count)
	}
}

func TestUnansweredHeartbeatAndSlowWriter(t *testing.T) {
	spec := launchTest(t, "echo")
	spec.Heartbeat = 80 * time.Millisecond
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	raw, ready := rawController(t, h, spec, 0)
	if ready.Type != "hello" {
		t.Fatal(ready.Type)
	}
	raw.SetReadDeadline(time.Now().Add(time.Second))
	f, err := ReadFrame(raw)
	if err != nil || f.Type != "health" {
		t.Fatalf("missing ping %s %v", f.Type, err)
	}
	var b struct {
		Ping string `json:"ping"`
	}
	json.Unmarshal(f.Body, &b)
	if b.Ping == "" {
		t.Fatal("empty heartbeat nonce")
	}
	WriteFrame(raw, Frame{Major: 1, Type: "health", Session: spec.Session, Body: body(map[string]string{"pong": b.Ping})})
	raw.SetReadDeadline(time.Time{})
	h.mu.Lock()
	controller := h.controller
	h.mu.Unlock()
	controller.socket.SetWriteBuffer(1024)
	raw.SetReadBuffer(1024)
	e := h.journal.Snapshot()[0]
	e.Payload = body(strings.Repeat("x", 300000))
	controller.enqueue(controller.frame("event", "", map[string]any{"event": e}))
	select {
	case <-controller.done:
	case <-time.After(time.Second):
		t.Fatal("stalled real socket writer not disconnected")
	}
	select {
	case <-h.Done():
		t.Fatal("stalled observer/controller killed child")
	default:
	}
}

// This mode helper is called only from the marked fake-child test binary.
func escapedChild() {
	child := exec.Command(os.Args[0], "-test.run=^TestFakeProvider$")
	child.Env = []string{"SHIM_TEST_CHILD=escaped-sleep", "HOME=" + os.Getenv("HOME"), "GORACE=atexit_sleep_ms=0"}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(4)
	}
	fmt.Fprintf(os.Stdout, "escaped:%d\n", child.Process.Pid)
}

func TestActualDisplacedControllerIsFenced(t *testing.T) {
	h, spec := hostTest(t, "echo")
	oldClient := clientTest(t, h, spec, "controller")
	h.mu.Lock()
	old := h.controller
	h.mu.Unlock()
	next, err := Connect(h.SocketPath(), spec.Secret, spec.Session, spec.Instance, "1", "controller", true)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	<-old.served
	// The retired socket is closed, but a command already decoded from that
	// connection still must pass the actual controller-object fence.
	replies := old.dispatch(Frame{Major: 1, Type: "inject", RequestID: "displaced", Session: spec.Session, Epoch: oldClient.Epoch, Body: body(injection(spec, "displaced", "must-not-write"))})
	if len(replies) != 1 || replies[0].Type != "error" {
		t.Fatalf("displaced reply: %+v", replies)
	}
	var b struct {
		Code string `json:"code"`
	}
	json.Unmarshal(replies[0].Body, &b)
	if b.Code != "stale_controller" {
		t.Fatal(b.Code)
	}
	h.op.Lock()
	_, executed := h.receipts["displaced"]
	h.op.Unlock()
	if executed {
		t.Fatal("displaced connection executed input")
	}
}

func TestLeaderExitKillsStubbornGroupDescendant(t *testing.T) {
	h, _ := hostTest(t, "tree-stubborn")
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var output []byte
		for _, e := range h.journal.Snapshot() {
			if e.Kind == "shim.output" {
				var b struct {
					Data string `json:"data"`
				}
				json.Unmarshal(e.Payload, &b)
				raw, _ := base64.StdEncoding.DecodeString(b.Data)
				output = append(output, raw...)
			}
		}
		for _, line := range strings.Split(string(output), "\n") {
			fmt.Sscanf(line, "descendant:%d", &pid)
		}
		if pid > 0 && bytes.Contains(output, []byte("ready")) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("missing descendant identity")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	h.stop("test")
	exit := h.Wait()
	if exit.Signal != int(syscall.SIGTERM) {
		t.Fatalf("signal death lost: %+v", exit)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("SIGTERM-ignoring descendant survived leader exit")
	}
	if err := h.sendSignal(syscall.SIGKILL); codeOf(err) != "target_offline" {
		t.Fatalf("signal after reap: %v", err)
	}
}

func TestJournalPathWithGlobMetacharacters(t *testing.T) {
	spec := launchTest(t, "echo")
	dir := filepath.Join(spec.Cwd, "journal[1]")
	j, err := OpenJournal(dir, spec.Session, 1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	e := mesh.Event{SchemaVersion: "1", ID: "event", Kind: "shim.marker", Time: time.Now(), Actor: spec.Actor, Subject: spec.Subject, Generation: 1, Payload: body("evidence")}
	saved, err := j.Append(e, false)
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = OpenJournal(dir, spec.Session, 1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if j.HighWater() != saved.Cursor {
		t.Fatal("glob path lost journal evidence")
	}
}

func TestRecordLeavesReplayFramingRoom(t *testing.T) {
	spec := launchTest(t, "echo")
	j, err := OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := mesh.Event{SchemaVersion: "1", ID: "event", Kind: "shim.marker", Time: time.Now(), Actor: spec.Actor, Subject: spec.Subject, Generation: 1, Payload: body(strings.Repeat("x", MaxFrame-1000))}
	if _, err = j.Append(e, false); codeOf(err) != "invalid_request" {
		t.Fatalf("unstreamable record accepted: %v", err)
	}
}

func TestZeroSignalAndDelegateBounds(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	req := injection(spec, "zero", "")
	req.Mode = "signal"
	req.Signal = 0
	c.Send(spec.Session, "inject", req)
	if code := resultCode(t, c); code != "invalid_request" {
		t.Fatal(code)
	}
	req = injection(spec, "delegates", "")
	for i := 0; i < 65; i++ {
		req.OnBehalfOf = append(req.OnBehalfOf, "msg://user/test/person")
	}
	c.Send(spec.Session, "inject", req)
	var b struct {
		Code string `json:"code"`
	}
	json.Unmarshal(receive(t, c, "error").Body, &b)
	if b.Code != "invalid_request" {
		t.Fatal(b.Code)
	}
	c.Send(spec.Session, "inject", injection(spec, "healthy", "ok"))
	if resultCode(t, c) != "bytes_written" {
		t.Fatal("validation killed child")
	}
}

func TestRejectedEventIsNotFatalToHost(t *testing.T) {
	h, spec := hostTest(t, "echo")
	bad := h.event("shim.rejected", strings.Repeat("x", MaxFrame))
	_, err := h.journal.Append(bad, false)
	if codeOf(err) != "invalid_request" {
		t.Fatal(err)
	}
	h.failJournal(err)
	select {
	case <-h.Done():
		t.Fatal("validation treated as physical journal failure")
	default:
	}
	c := clientTest(t, h, spec, "controller")
	c.Send(spec.Session, "inject", injection(spec, "after-reject", "ok"))
	if resultCode(t, c) != "bytes_written" {
		t.Fatal("valid effect refused")
	}
}

func TestHeartbeatPingPongKeepsConnection(t *testing.T) {
	spec := launchTest(t, "echo")
	spec.Heartbeat = 30 * time.Millisecond
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	raw, ready := rawController(t, h, spec, 0)
	if ready.Type != "hello" {
		t.Fatal(ready.Type)
	}
	for i := 0; i < 5; i++ {
		raw.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		f, err := ReadFrame(raw)
		if err != nil || f.Type != "health" {
			t.Fatalf("ping %d: %s %v", i, f.Type, err)
		}
		var b struct {
			Ping string `json:"ping"`
		}
		json.Unmarshal(f.Body, &b)
		if b.Ping == "" {
			t.Fatal("missing nonce")
		}
		if err = WriteFrame(raw, Frame{Major: 1, Type: "health", Session: spec.Session, Body: body(map[string]string{"pong": b.Ping})}); err != nil {
			t.Fatal(err)
		}
	}
	if err = WriteFrame(raw, Frame{Major: 1, Type: "health", RequestID: "probe", Session: spec.Session, Body: body(map[string]string{"ping": "probe"})}); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := ReadFrame(raw)
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == "result" && f.ReplyTo == "probe" {
			var b struct {
				Running bool `json:"running"`
			}
			json.Unmarshal(f.Body, &b)
			if !b.Running {
				t.Fatal("pong did not preserve healthy session")
			}
			return
		}
	}
}
