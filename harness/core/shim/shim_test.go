package shim

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

// The executable is the test binary itself, never a model CLI. All execution
// modes require a dedicated child marker and a home in the test's private tree.
func TestFakeProvider(t *testing.T) {
	mode := os.Getenv("SHIM_TEST_CHILD")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		fmt.Fprintln(os.Stdout, "ready")
		io.Copy(os.Stdout, os.Stdin)
	case "burst":
		b := bytes.Repeat([]byte("z"), OutputChunk)
		for i := 0; i < 80; i++ {
			os.Stdout.Write(b)
		}
	case "tree":
		child := exec.Command(os.Args[0], "-test.run=^TestFakeProvider$")
		child.Env = []string{"SHIM_TEST_CHILD=sleep", "HOME=" + os.Getenv("HOME")}
		if child.Start() != nil {
			os.Exit(3)
		}
		fmt.Fprintf(os.Stdout, "descendant:%d\n", child.Process.Pid)
		io.Copy(io.Discard, os.Stdin)
	case "sleep":
		for {
			time.Sleep(time.Hour)
		}
	case "stubborn":
		signalIgnore()
		fmt.Fprintln(os.Stdout, "ready")
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(0)
}
func signalIgnore() { ignoreTermination() }
func launchTest(t *testing.T, mode string) Launch {
	t.Helper()
	root, err := os.MkdirTemp(os.TempDir(), "s-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.WriteFile(filepath.Join(root, "test.pin"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Launch{Session: "urn:session:test", Instance: "urn:instance:test", Generation: 1, Actor: mesh.Actor{URN: "msg://service/shim/test", Kind: mesh.ActorService}, Subject: "urn:session:test", Argv: []string{exe, "-test.run=^TestFakeProvider$"}, Env: []string{"SHIM_TEST_CHILD=" + mode, "HOME=" + root}, Cwd: root, ControlDir: filepath.Join(root, "c"), JournalDir: filepath.Join(root, "j"), Secret: strings.Repeat("s", 32), PinPath: filepath.Join(root, "test.pin"), PinKey: "test", BootGeneration: "boot-1", Reservation: "reserve-1", StopGrace: 50 * time.Millisecond, Heartbeat: time.Second, JournalBytes: 16 << 20, ClientQueue: 256}
}
func hostTest(t *testing.T, mode string) (*Host, Launch) {
	t.Helper()
	spec := launchTest(t, mode)
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, spec
}
func clientTest(t *testing.T, h *Host, spec Launch, role string) *Client {
	t.Helper()
	c, err := Connect(h.SocketPath(), spec.Secret, spec.Session, spec.Instance, "1", role, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func receive(t *testing.T, c *Client, kind string) Frame {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-c.Frames:
			if !ok {
				t.Fatal("client disconnected")
			}
			if f.Type == kind {
				return f
			}
		case <-timer.C:
			t.Fatal("timed out waiting for " + kind)
		}
	}
}
func resultCode(t *testing.T, c *Client) string {
	t.Helper()
	f := receive(t, c, "result")
	var b struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(f.Body, &b); err != nil {
		t.Fatal(err)
	}
	return b.Code
}
func injection(spec Launch, key, data string) Inject {
	return Inject{Key: key, Actor: spec.Actor, Subject: spec.Subject, Mode: "input", Delivery: "immediate", Data: base64.StdEncoding.EncodeToString([]byte(data)), Generation: "1"}
}
func awaitKind(t *testing.T, h *Host, kind string) mesh.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range h.journal.Snapshot() {
			if e.Kind == kind {
				return e
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("event missing: " + kind)
	return mesh.Event{}
}

func TestFrameBoundaries(t *testing.T) {
	f := Frame{Major: 1, Type: "hello", Session: "session", Body: body(map[string]string{"v": "ok"})}
	var b bytes.Buffer
	if err := WriteFrame(&b, f); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), b.Bytes()...)
	if err := WriteFrame(&b, f); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := ReadFrame(&b)
		if err != nil || got.Type != "hello" {
			t.Fatalf("coalesced: %v", err)
		}
	}
	got, err := ReadFrame(&singleByteReader{raw})
	if err != nil || got.Session != "session" {
		t.Fatalf("split: %v", err)
	}
	for _, n := range []uint32{0, MaxFrame + 1} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], n)
		if _, err := ReadFrame(bytes.NewReader(size[:])); err == nil {
			t.Fatal("accepted invalid frame length")
		}
	}
	for _, data := range [][]byte{raw[:len(raw)-1], {0, 0, 0, 1, '{'}} {
		if _, err := ReadFrame(bytes.NewReader(data)); err == nil {
			t.Fatal("accepted truncated/malformed frame")
		}
	}
}

type singleByteReader struct{ b []byte }

func (r *singleByteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	p[0] = r.b[0]
	r.b = r.b[1:]
	return 1, nil
}

func TestDisconnectReplaySameChild(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	pid := h.cmd.Process.Pid
	awaitKind(t, h, "shim.output")
	if err := c.Replay(spec.Session, ""); err != nil {
		t.Fatal(err)
	}
	var cursor string
	for cursor == "" {
		f := receive(t, c, "event")
		var b struct {
			Event mesh.Event `json:"event"`
		}
		json.Unmarshal(f.Body, &b)
		if b.Event.Kind == "shim.output" {
			cursor = b.Event.Cursor
		}
	}
	c.Close()
	// Output produced with no controller remains in the same durable journal.
	detached := []byte{0, 'a', 255, 'b', '\n'}
	if _, err := h.stdin.Write(detached); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.journal.Snapshot()) >= 6 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c2 := clientTest(t, h, spec, "controller")
	if c2.Epoch == c.Epoch {
		t.Fatal("reconnect did not fence prior epoch")
	}
	if h.cmd.Process.Pid != pid {
		t.Fatal("reconnect respawned child")
	}
	if err := c2.Replay(spec.Session, cursor); err != nil {
		t.Fatal(err)
	}
	for {
		f := receive(t, c2, "event")
		var b struct {
			Event mesh.Event `json:"event"`
		}
		json.Unmarshal(f.Body, &b)
		if b.Event.Kind != "shim.output" {
			continue
		}
		var out struct {
			Data string `json:"data"`
		}
		json.Unmarshal(b.Event.Payload, &out)
		raw, _ := base64.StdEncoding.DecodeString(out.Data)
		if !bytes.Equal(raw, detached) {
			t.Fatalf("lost bytes: %q", raw)
		}
		break
	}
}

func TestInputIdempotencyAndRefusals(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	req := injection(spec, "once", "payload\n")
	for i := 0; i < 2; i++ {
		if err := c.Send(spec.Session, "inject", req); err != nil {
			t.Fatal(err)
		}
		if got := resultCode(t, c); got != "bytes_written" {
			t.Fatal(got)
		}
	}
	req.Data = base64.StdEncoding.EncodeToString([]byte("different"))
	c.Send(spec.Session, "inject", req)
	var b struct {
		Code string `json:"code"`
	}
	json.Unmarshal(receive(t, c, "error").Body, &b)
	if b.Code != "idempotency_conflict" {
		t.Fatal(b.Code)
	}
	req = injection(spec, "turn", "")
	req.Mode = "turn"
	c.Send(spec.Session, "inject", req)
	if got := resultCode(t, c); got != "unsupported_mode" {
		t.Fatal(got)
	}
	req = injection(spec, "idle", "")
	req.Delivery = "at_idle"
	c.Send(spec.Session, "inject", req)
	if got := resultCode(t, c); got != "unsupported_delivery" {
		t.Fatal(got)
	}
	req = injection(spec, "old", "")
	req.Generation = "2"
	c.Send(spec.Session, "inject", req)
	if got := resultCode(t, c); got != "stale_generation" {
		t.Fatal(got)
	}
	// A durable uncertain receipt returns unchanged and never touches stdin.
	h.op.Lock()
	uncertain := injection(spec, "uncertain", "forbidden")
	sum := fingerprintForTest(uncertain)
	h.receipts[uncertain.Key] = receipt{Fingerprint: sum, Code: "outcome_unknown", Cursor: h.journal.HighWater()}
	h.op.Unlock()
	c.Send(spec.Session, "inject", uncertain)
	if got := resultCode(t, c); got != "outcome_unknown" {
		t.Fatal(got)
	}
}

func TestPinOverlapAndExit(t *testing.T) {
	spec := launchTest(t, "echo")
	caller, err := openLock(spec.PinPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if f, err := openLock(spec.PinPath, true); err == nil {
		f.Close()
		t.Fatal("mutator acquired during overlapping holds")
	}
	caller.Close()
	if f, err := openLock(spec.PinPath, true); err == nil {
		f.Close()
		t.Fatal("shim lost pin after caller release")
	}
	c := clientTest(t, h, spec, "controller")
	var hello map[string]any
	json.Unmarshal(c.Hello, &hello)
	if hello["pin_adopted"] != true {
		t.Fatal("missing adoption acknowledgment")
	}
	c.Send(spec.Session, "control", map[string]any{"action": "kill", "expected_generation": "1"})
	if got := resultCode(t, c); got != "submitted" {
		t.Fatal(got)
	}
	h.Wait()
	c.Send(spec.Session, "control", map[string]any{"action": "signal", "signal": 15, "expected_generation": "1"})
	var offline struct {
		Code string `json:"code"`
	}
	json.Unmarshal(receive(t, c, "error").Body, &offline)
	if offline.Code != "target_offline" {
		t.Fatal(offline.Code)
	}
	f, err := openLock(spec.PinPath, true)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestObserverTakeoverAndFences(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	observer := clientTest(t, h, spec, "observer")
	observer.Send(spec.Session, "inject", injection(spec, "no", "no"))
	var b struct {
		Code string `json:"code"`
	}
	json.Unmarshal(receive(t, observer, "error").Body, &b)
	if b.Code != "read_only" {
		t.Fatal(b.Code)
	}
	if other, err := Connect(h.SocketPath(), spec.Secret, spec.Session, spec.Instance, "1", "controller", false); err == nil {
		other.Close()
		t.Fatal("second controller accepted")
	}
	c2, err := Connect(h.SocketPath(), spec.Secret, spec.Session, spec.Instance, "1", "controller", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.Epoch == c.Epoch {
		t.Fatal("takeover did not advance epoch")
	}
	c2.SendFrame(Frame{Major: 1, Type: "inject", Session: spec.Session, Epoch: c.Epoch, Body: body(injection(spec, "stale", "no"))})
	json.Unmarshal(receive(t, c2, "error").Body, &b)
	if b.Code != "stale_controller" {
		t.Fatal(b.Code)
	}
	if other, err := Connect(h.SocketPath(), "wrong", spec.Session, spec.Instance, "1", "observer", false); err == nil {
		other.Close()
		t.Fatal("bad secret accepted")
	}
}

func TestReplayTailOrdering(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	c.Replay(spec.Session, "")
	receive(t, c, "result")
	go func() {
		for i := 0; i < 25; i++ {
			h.record("shim.test_marker", map[string]int{"index": i}, false)
		}
	}()
	var last uint64
	markers := 0
	for markers < 25 {
		f := receive(t, c, "event")
		var b struct {
			Event mesh.Event `json:"event"`
		}
		json.Unmarshal(f.Body, &b)
		seq, err := h.journal.parseSafe(b.Event.Cursor)
		if err != nil || seq != last+1 {
			t.Fatalf("ordered replay: %d after %d: %v", seq, last, err)
		}
		last = seq
		if b.Event.Kind == "shim.test_marker" {
			markers++
		}
	}
}

func TestJournalRecoveryAndCorruption(t *testing.T) {
	spec := launchTest(t, "echo")
	j, err := OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	e := mesh.Event{SchemaVersion: "1", ID: "event", Kind: "shim.test", Time: time.Now(), Actor: spec.Actor, Subject: spec.Subject, Generation: 1, Payload: body(map[string]string{"v": "ok"})}
	original, err := j.Append(e, false)
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	files, _ := filepath.Glob(filepath.Join(spec.JournalDir, "*.seg"))
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0, 0})
	f.Close()
	j, err = OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if j.HighWater() != original.Cursor {
		t.Fatal("torn recovery changed cursor")
	}
	quarantine, _ := filepath.Glob(files[0] + ".torn-*")
	if len(quarantine) == 0 {
		t.Fatal("lost torn evidence")
	}
	j.Close()
	f, err = os.OpenFile(files[0], os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte{'!'}, int64(len(segmentHeader)+5))
	f.Close()
	if j, err = OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20); err == nil {
		j.Close()
		t.Fatal("mid-segment corruption accepted")
	} else if codeOf(err) != "journal_corrupt" {
		t.Fatal(err)
	}
}
func TestJournalFsyncAndSlowSubscriber(t *testing.T) {
	spec := launchTest(t, "echo")
	j, err := OpenJournal(spec.JournalDir, spec.Session, 1, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	_, sub, err := j.Subscribe("", 1)
	if err != nil {
		t.Fatal(err)
	}
	event := mesh.Event{SchemaVersion: "1", ID: "event", Kind: "shim.test", Time: time.Now(), Actor: spec.Actor, Subject: spec.Subject, Generation: 1, Payload: body("data")}
	j.Append(event, false)
	j.Append(event, false)
	select {
	case <-sub.done:
	default:
		t.Fatal("slow subscriber not disconnected")
	}
	before := j.HighWater()
	j.syncFile = func(*os.File) error { return errors.New("simulated sync failure") }
	if _, err = j.Append(event, false); codeOf(err) != "journal_unavailable" {
		t.Fatalf("sync failure: %v", err)
	}
	if j.HighWater() != before {
		t.Fatal("uncommitted cursor published")
	}
}
func TestJournalCapacityStopsChild(t *testing.T) {
	spec := launchTest(t, "burst")
	spec.JournalBytes = 2 << 20
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("journal cap did not stop child")
	}
	if h.Wait().Cause != "journal_failure" {
		t.Fatal(h.Wait())
	}
	gap := awaitKind(t, h, "shim.output_gap")
	if !gap.Truncated {
		t.Fatal("missing gap/truncation evidence")
	}
}
func TestWallTimeAndEscalatedTreeKill(t *testing.T) {
	t.Run("wall", func(t *testing.T) {
		spec := launchTest(t, "stubborn")
		spec.WallTime = 150 * time.Millisecond
		h, err := Start(spec)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		select {
		case <-h.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("wall time not enforced")
		}
		if h.Wait().Cause != "wall_time" {
			t.Fatal(h.Wait())
		}
	})
	t.Run("tree", func(t *testing.T) {
		h, _ := hostTest(t, "tree")
		e := awaitKind(t, h, "shim.output")
		var b struct {
			Data string `json:"data"`
		}
		json.Unmarshal(e.Payload, &b)
		data, _ := base64.StdEncoding.DecodeString(b.Data)
		pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(data), "descendant:")))
		if err != nil {
			t.Fatal(err)
		}
		h.stop("test")
		h.Wait()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if !processAlive(pid) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("descendant %d survived", pid)
	})
}
func TestMalformedClientAndNegotiation(t *testing.T) {
	h, spec := hostTest(t, "echo")
	for _, major := range []int{99, 1} {
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.SocketPath(), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		challenge, err := ReadFrame(c)
		if err != nil {
			t.Fatal(err)
		}
		var b struct {
			Nonce string `json:"nonce"`
		}
		json.Unmarshal(challenge.Body, &b)
		WriteFrame(c, Frame{Major: major, Type: "hello", Session: spec.Session, Body: body(Hello{Major: major, Minor: 0, Role: "controller", Proof: Proof(spec.Secret, b.Nonce, spec.Session, "controller"), Instance: spec.Instance, Generation: "1"})})
		f, err := ReadFrame(c)
		if err != nil {
			t.Fatal(err)
		}
		if major == 99 && f.Type != "error" {
			t.Fatal("unsupported protocol accepted")
		}
		if major == 1 {
			if f.Type != "hello" {
				t.Fatal("baseline protocol refused")
			}
			c.Write([]byte{0, 0, 0, 1, '{'})
		}
		c.Close()
	}
	c := clientTest(t, h, spec, "controller")
	c.Send(spec.Session, "health", map[string]string{"ping": "probe"})
	receive(t, c, "result")
	h.mu.Lock()
	running := h.running
	h.mu.Unlock()
	if !running {
		t.Fatal("malformed client killed child")
	}
}

func TestUncertainPartialInputIsNotRetried(t *testing.T) {
	h, spec := hostTest(t, "echo")
	awaitKind(t, h, "shim.output")
	h.op.Lock()
	partial := &partialDriver{transportDriver: h.driver}
	h.driver = partial
	h.op.Unlock()
	c := clientTest(t, h, spec, "controller")
	req := injection(spec, "partial", strings.Repeat("x", OutputChunk))
	c.Send(spec.Session, "inject", req)
	first := receive(t, c, "result")
	var receipt1 receipt
	json.Unmarshal(first.Body, &receipt1)
	if receipt1.Code != "outcome_unknown" {
		t.Fatalf("blocked raw input: %+v", receipt1)
	}
	c.Send(spec.Session, "inject", req)
	second := receive(t, c, "result")
	var receipt2 receipt
	json.Unmarshal(second.Body, &receipt2)
	h.op.Lock()
	calls := partial.calls
	h.op.Unlock()
	if calls != 1 {
		t.Fatal("uncertain effect retried")
	}
	if receipt2 != receipt1 {
		t.Fatalf("retry changed uncertain receipt: %+v / %+v", receipt1, receipt2)
	}
}

func TestHeartbeatDetachesWithoutKillingChild(t *testing.T) {
	spec := launchTest(t, "echo")
	spec.Heartbeat = 30 * time.Millisecond
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: h.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	challenge, err := ReadFrame(c)
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Nonce string `json:"nonce"`
	}
	json.Unmarshal(challenge.Body, &b)
	WriteFrame(c, Frame{Major: 1, Type: "hello", Session: spec.Session, Body: body(Hello{Major: 1, Role: "controller", Proof: Proof(spec.Secret, b.Nonce, spec.Session, "controller"), Instance: spec.Instance, Generation: "1"})})
	if f, err := ReadFrame(c); err != nil || f.Type != "hello" {
		t.Fatalf("hello: %v", err)
	}
	time.Sleep(160 * time.Millisecond)
	select {
	case <-h.Done():
		t.Fatal("heartbeat timeout killed child")
	default:
	}
	next := clientTest(t, h, spec, "controller")
	next.Send(spec.Session, "health", map[string]string{"ping": "still-live"})
	receive(t, next, "result")
}

func TestAckBoundsAndPinnedJournalIdentity(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	c.Ack(spec.Session, h.journal.HighWater())
	f := receive(t, c, "error")
	var errorBody struct {
		Code string `json:"code"`
	}
	json.Unmarshal(f.Body, &errorBody)
	if errorBody.Code != "cursor_invalid" {
		t.Fatal(errorBody.Code)
	}
	c.Replay(spec.Session, "")
	receive(t, c, "result")
	output := receive(t, c, "event")
	var b struct {
		Event mesh.Event `json:"event"`
	}
	json.Unmarshal(output.Body, &b)

	c.Ack(spec.Session, b.Event.Cursor)
	receive(t, c, "result")
	c.Ack(spec.Session, "foreign:1")
	json.Unmarshal(receive(t, c, "error").Body, &errorBody)
	if errorBody.Code != "cursor_invalid" {
		t.Fatal(errorBody.Code)
	}
	c.Replay(spec.Session, c.Journal+":999999")
	json.Unmarshal(receive(t, c, "error").Body, &errorBody)
	if errorBody.Code != "cursor_invalid" {
		t.Fatal(errorBody.Code)
	}
}

func TestLaunchAndJournalRefuseUnsafeReuse(t *testing.T) {
	spec := launchTest(t, "echo")
	h, err := Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	other := spec
	other.ControlDir = filepath.Join(spec.Cwd, "other")
	if duplicate, err := Start(other); err == nil {
		duplicate.Close()
		t.Fatal("duplicate host spawned")
	}
	h.Close()
	if duplicate, err := Start(other); err == nil {
		duplicate.Close()
		t.Fatal("old launch journal spawned child again")
	} else if codeOf(err) != "outcome_unknown" {
		t.Fatal(err)
	}
	missing := launchTest(t, "echo")
	os.Remove(missing.PinPath)
	if unexpected, err := Start(missing); err == nil {
		unexpected.Close()
		t.Fatal("shim created an unreserved pin")
	}
	symlink := launchTest(t, "echo")
	os.Remove(symlink.PinPath)
	target := filepath.Join(symlink.Cwd, "target")
	os.WriteFile(target, nil, 0600)
	os.Symlink(target, symlink.PinPath)
	if unexpected, err := Start(symlink); err == nil {
		unexpected.Close()
		t.Fatal("symlink pin accepted")
	}
}

func TestDetachedCaptureDrainsOnExit(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "controller")
	c.Close()
	want := bytes.Repeat([]byte("tail\x00\xff"), 20000)
	go func() { h.stdin.Write(want); h.stdin.Close() }()
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("echo child did not exit")
	}
	var got []byte
	for _, e := range h.journal.Snapshot() {
		if e.Kind != "shim.output" {
			continue
		}
		var out struct {
			Stream string `json:"stream"`
			Data   string `json:"data"`
		}
		json.Unmarshal(e.Payload, &out)
		if out.Stream == "stdout" {
			data, _ := base64.StdEncoding.DecodeString(out.Data)
			got = append(got, data...)
		}
	}
	if !bytes.Equal(got, append([]byte("ready\n"), want...)) {
		t.Fatalf("exit dropped output: got %d want %d", len(got), len(want)+6)
	}
}

type partialDriver struct {
	transportDriver
	calls int
}

func (d *partialDriver) WriteInput(data []byte) (int, error) {
	d.calls++
	n, err := d.transportDriver.WriteInput(data[:3])
	if err != nil {
		return n, err
	}
	return n, io.ErrShortWrite
}
