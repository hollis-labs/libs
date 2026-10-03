package shim

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

type Hello struct {
	Major      int    `json:"major"`
	Minor      int    `json:"minor"`
	Role       string `json:"role"`
	Proof      string `json:"proof"`
	Instance   string `json:"instance"`
	Generation string `json:"generation"`
	Journal    string `json:"journal,omitempty"`
	Epoch      string `json:"controller_epoch,omitempty"`
	Takeover   bool   `json:"takeover,omitempty"`
	Attached   bool   `json:"attached,omitempty"`
}

type connection struct {
	served         chan struct{}
	host           *Host
	socket         *net.UnixConn
	role           string
	epoch          uint64
	send           chan Frame
	done           chan struct{}
	once           sync.Once
	replayStop     chan struct{}
	replayDone     chan struct{}
	delivered      string
	ack            string
	mu             sync.Mutex
	pending        []Frame
	deferResponses bool
}

func (c *connection) close() { c.once.Do(func() { close(c.done); c.socket.Close() }) }
func (c *connection) enqueue(f Frame) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- f:
		return true
	default:
		c.close()
		return false
	}
}
func (c *connection) frame(kind, reply string, v any) Frame {
	return Frame{Major: ProtocolMajor, Minor: ProtocolMinor, Type: kind, ReplyTo: reply, Session: c.host.launch.Session, Epoch: fmt.Sprint(c.epoch), Body: body(v)}
}
func (c *connection) respond(f Frame) {
	if c.deferResponses {
		c.pending = append(c.pending, f)
		return
	}
	c.enqueueWait(f, nil)
}
func (c *connection) result(f Frame, v any) { c.respond(c.frame("result", f.RequestID, v)) }
func (c *connection) error(f Frame, err error) {
	details := map[string]any{"code": codeOf(err), "message": err.Error(), "retryable": false}
	if codeOf(err) == "unsupported_protocol" {
		details["supported_versions"] = []map[string]int{{"major": ProtocolMajor, "minor_min": 0, "minor_max": ProtocolMinor}}
	}
	c.respond(c.frame("error", f.RequestID, details))
}

func (h *Host) accept() {
	defer h.wg.Done()
	for {
		socket, err := h.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case h.connectionSlots <- struct{}{}:
		default:
			socket.Close()
			continue
		}
		h.wg.Add(1)
		go func() { defer h.wg.Done(); defer func() { <-h.connectionSlots }(); h.serve(socket) }()
	}
}
func (h *Host) serve(socket *net.UnixConn) {
	defer socket.Close()
	if err := checkPeer(socket); err != nil {
		return
	}
	socket.SetDeadline(time.Now().Add(5 * time.Second))
	nonce := newID()
	h.mu.Lock()
	epoch := h.epoch
	h.mu.Unlock()
	challenge := Frame{Major: ProtocolMajor, Minor: ProtocolMinor, Type: "hello", Session: h.launch.Session, Body: body(map[string]any{"nonce": nonce, "controller_epoch": fmt.Sprint(epoch)})}
	if err := WriteFrame(socket, challenge); err != nil {
		return
	}
	f, err := ReadFrame(socket)
	if err != nil {
		return
	}
	var hello Hello
	reject := func(code string) {
		details := map[string]any{"code": code, "message": "hello refused: " + code, "retryable": false}
		if code == "unsupported_protocol" {
			details["supported_versions"] = []map[string]int{{"major": ProtocolMajor, "minor_min": 0, "minor_max": ProtocolMinor}}
		}
		WriteFrame(socket, Frame{Major: ProtocolMajor, Minor: ProtocolMinor, Type: "error", ReplyTo: f.RequestID, Session: h.launch.Session, Body: body(details)})
	}
	if f.Type == "auth" {
		reject("attached_mode_not_supported")
		return
	}
	if f.Type != "hello" || json.Unmarshal(f.Body, &hello) != nil {
		reject("invalid_request")
		return
	}
	if hello.Attached {
		reject("attached_mode_not_supported")
		return
	}
	if hello.Major != ProtocolMajor || hello.Minor < 0 || f.Major != ProtocolMajor {
		reject("unsupported_protocol")
		return
	}
	if hello.Role != "controller" && hello.Role != "observer" {
		reject("invalid_request")
		return
	}
	if !hmac.Equal([]byte(hello.Proof), []byte(Proof(h.launch.Secret, nonce, h.launch.Session, hello.Role))) {
		reject("unauthorized")
		return
	}
	if f.Session != h.launch.Session || hello.Instance != h.launch.Instance || hello.Generation != fmt.Sprint(h.launch.Generation) || (hello.Journal != "" && hello.Journal != h.journal.identity.ID) {
		reject("identity_mismatch")
		return
	}
	c := &connection{host: h, socket: socket, role: hello.Role, send: make(chan Frame, h.launch.ClientQueue), done: make(chan struct{}), served: make(chan struct{})}
	defer close(c.served)
	h.op.Lock()
	h.mu.Lock()
	select {
	case <-h.closing:
		h.mu.Unlock()
		h.op.Unlock()
		return
	default:
	}
	if c.role == "observer" {
		if h.observer != nil {
			select {
			case <-h.observer.done:
			default:
				h.mu.Unlock()
				h.op.Unlock()
				reject("observer_busy")
				return
			}
		}
		c.epoch = h.epoch
		h.mu.Unlock()
		if _, err = h.record("shim.attached", map[string]string{"role": "observer", "epoch": fmt.Sprint(c.epoch)}, false); err != nil {
			h.op.Unlock()
			reject(codeOf(err))
			h.failJournal(err)
			return
		}
		h.mu.Lock()
		select {
		case <-h.closing:
			c.close()
			h.mu.Unlock()
			h.op.Unlock()
			return
		default:
		}
		h.observer = c
	} else {
		if h.controller != nil {
			select {
			case <-h.controller.done:
			default:
				if !hello.Takeover || hello.Epoch != fmt.Sprint(h.epoch) {
					h.mu.Unlock()
					h.op.Unlock()
					reject("controller_busy")
					return
				}
			}
		}
		nextEpoch := h.epoch + 1
		h.mu.Unlock()
		// The takeover event is durable before any new epoch is published.
		if _, err = h.record("shim.attached", map[string]string{"role": "controller", "epoch": fmt.Sprint(nextEpoch)}, false); err != nil {
			h.op.Unlock()
			reject("journal_unavailable")
			h.failJournal(err)
			return
		}
		h.mu.Lock()
		select {
		case <-h.closing:
			c.close()
			h.mu.Unlock()
			h.op.Unlock()
			return
		default:
		}
		if h.controller != nil {
			h.controller.close()
		}
		h.epoch++
		c.epoch = h.epoch
		h.controller = c
	}
	h.mu.Unlock()
	h.op.Unlock()
	defer func() {
		c.close()
		if _, err := h.record("shim.detached", map[string]string{"role": c.role, "epoch": fmt.Sprint(c.epoch)}, false); err != nil {
			h.failJournal(err)
		}
	}()
	socket.SetDeadline(time.Time{})
	info := map[string]any{"protocol_major": ProtocolMajor, "protocol_minor": ProtocolMinor, "journal": h.journal.identity.ID, "generation": fmt.Sprint(h.launch.Generation), "instance": h.launch.Instance, "controller_epoch": fmt.Sprint(c.epoch), "max_frame": MaxFrame, "pin_adopted": true, "pin_key": h.launch.PinKey, "boot_generation": h.launch.BootGeneration, "reservation": h.launch.Reservation, "capabilities": map[string]any{"inject": []string{"input/immediate", "signal/immediate"}, "control": []string{"kill", "signal"}, "driver": "stdio", "process_boundary": "process_group", "mandatory_child_isolation": false, "peer_uid": runtime.GOOS == "linux", "limits": []string{"wall_time", "journal_bytes"}, "attached": false}}
	if err = WriteFrame(socket, c.frame("hello", f.RequestID, info)); err != nil {
		return
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); c.writeLoop() }()
	go func() {
		defer workers.Done()
		timer := time.NewTicker(h.launch.Heartbeat)
		defer timer.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-timer.C:
				c.enqueueWait(c.frame("health", "", map[string]any{"ping": newID(), "status": h.health()}), nil)
			}
		}
	}()
	defer func() { c.close(); c.stopReplay(); workers.Wait() }()
	for {
		socket.SetReadDeadline(time.Now().Add(3 * h.launch.Heartbeat))
		f, err = ReadFrame(socket)
		if err != nil {
			return
		}
		if len(f.RequestID) > 256 {
			f.RequestID = ""
			c.error(f, fault("invalid_request", "request identifier too large"))
			continue
		}
		if f.Session != h.launch.Session || f.Major != ProtocolMajor || f.Minor < 0 || f.Minor > ProtocolMinor {
			c.error(f, fault("unsupported_protocol", "session or protocol mismatch"))
			continue
		}
		if f.Type == "health" {
			var ping struct {
				Ping string `json:"ping"`
			}
			if json.Unmarshal(f.Body, &ping) != nil {
				c.error(f, fault("invalid_request", "bad health body"))
				continue
			}
			if ping.Ping != "" {
				c.result(f, h.health())
			}
			continue
		}
		if f.Type == "auth" {
			c.error(f, fault("attached_mode_not_supported", "attached authentication reserved"))
			continue
		}
		if f.Type == "replay" {
			c.replay(f)
			continue
		}
		if c.role != "controller" {
			c.journalRefusal(f, fault("read_only", "observer cannot execute commands"))
			continue
		}
		for _, reply := range c.dispatch(f) {
			if !c.enqueueWait(reply, nil) {
				return
			}
		}
	}
}
func (c *connection) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case f := <-c.send:
			if f.Type == "event" {
				var b struct {
					Event mesh.Event `json:"event"`
				}
				json.Unmarshal(f.Body, &b)
				c.mu.Lock()
				c.delivered = b.Event.Cursor
				c.mu.Unlock()
			}
			c.socket.SetWriteDeadline(time.Now().Add(3 * c.host.launch.Heartbeat))
			if err := WriteFrame(c.socket, f); err != nil {
				c.close()
				return
			}
		}
	}
}
func (c *connection) stopReplay() {
	if c.replayStop != nil {
		close(c.replayStop)
		<-c.replayDone
		c.replayStop = nil
	}
}
func (c *connection) enqueueWait(f Frame, stop <-chan struct{}) bool {
	timer := time.NewTimer(3 * c.host.launch.Heartbeat)
	defer timer.Stop()
	select {
	case <-c.done:
		return false
	case <-stop:
		return false
	case c.send <- f:
		return true
	case <-timer.C:
		c.close()
		return false
	}
}
func (c *connection) replay(f Frame) {
	var req struct {
		After string `json:"after_cursor"`
	}
	if json.Unmarshal(f.Body, &req) != nil {
		c.error(f, fault("invalid_request", "bad replay request"))
		return
	}
	c.stopReplay()
	next, high, sub, err := c.host.journal.subscribe(req.After)
	if err != nil {
		c.error(f, err)
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	c.replayStop = stop
	c.replayDone = done
	go func() {
		defer close(done)
		defer c.host.journal.unsubscribe(sub)
		if !c.enqueueWait(c.frame("result", f.RequestID, map[string]string{"high_water": c.host.journal.cursor(high)}), stop) {
			return
		}
		for {
			batch := c.host.journal.readAfter(next, 32)
			for _, e := range batch {
				if !c.enqueueWait(c.frame("event", "", map[string]any{"event": e, "replay": next < high}), stop) {
					return
				}
				next++
			}
			if len(batch) > 0 {
				continue
			}
			select {
			case <-stop:
				return
			case <-c.done:
				return
			case <-sub.done:
				return
			case <-sub.notify:
				continue
			}
		}
	}()
}
func (c *connection) acknowledge(f Frame) {
	var req struct {
		Cursor string `json:"cursor"`
	}
	if json.Unmarshal(f.Body, &req) != nil {
		c.error(f, fault("invalid_request", "bad ack"))
		return
	}
	if err := c.host.journal.ValidateCursor(req.Cursor); err != nil {
		c.error(f, err)
		return
	}
	c.mu.Lock()
	delivered := c.delivered
	previous := c.ack
	c.mu.Unlock()
	seq, _ := c.host.journal.parseSafe(req.Cursor)
	sent, _ := c.host.journal.parseSafe(delivered)
	old, _ := c.host.journal.parseSafe(previous)
	if req.Cursor == "" || seq > sent || seq < old {
		c.error(f, fault("cursor_invalid", "ack must advance within delivered history"))
		return
	}
	// Ack is a local watermark, not an event. Streaming acknowledgments
	// would create an unbounded feedback loop for a conforming consumer.
	c.mu.Lock()
	c.ack = req.Cursor
	c.mu.Unlock()
	c.result(f, map[string]string{"cursor": req.Cursor})
}
func (j *Journal) parseSafe(cursor string) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.parse(cursor)
}

type Inject struct {
	Key        string     `json:"idempotency_key"`
	Actor      mesh.Actor `json:"actor"`
	OnBehalfOf []mesh.URN `json:"on_behalf_of,omitempty"`
	Subject    mesh.URN   `json:"subject"`
	Mode       string     `json:"mode"`
	Delivery   string     `json:"delivery"`
	Data       string     `json:"data,omitempty"`
	Signal     int        `json:"signal,omitempty"`
	Generation string     `json:"expected_generation"`
	Deadline   string     `json:"deadline,omitempty"`
}
type receipt struct {
	Fingerprint string `json:"fingerprint"`
	Code        string `json:"code"`
	Cursor      string `json:"cursor"`
	Bytes       int    `json:"bytes,omitempty"`
}

func (c *connection) inject(f Frame) {
	var req Inject
	if json.Unmarshal(f.Body, &req) != nil {
		c.journalRefusal(f, fault("invalid_request", "bad inject JSON"))
		return
	}
	if req.Delivery == "" {
		req.Delivery = "at_idle"
	}
	// Bound provenance before constructing durable events or fingerprints.
	if req.Actor.Validate() != nil || req.Subject.Validate() != nil || len(req.Actor.URN) > 1024 || len(req.Subject) > 1024 || len(req.OnBehalfOf) > 64 || req.Key == "" || len(req.Key) > 256 {
		c.journalRefusal(f, fault("invalid_request", "invalid or oversized provenance/key"))
		return
	}
	for _, u := range req.OnBehalfOf {
		if u.Validate() != nil || len(u) > 1024 {
			c.journalRefusal(f, fault("invalid_request", "invalid delegated actor"))
			return
		}
	}
	sum := sha256.Sum256(body(req))
	fingerprint := hex.EncodeToString(sum[:])
	h := c.host
	if old, ok := h.receipts[req.Key]; ok {
		if old.Fingerprint != fingerprint {
			c.journalRefusal(f, fault("idempotency_conflict", "key reused with different request"))
			return
		}
		retry := h.event("shim.inject_retry", map[string]string{"key": req.Key, "code": old.Code})
		retry.Actor = req.Actor
		retry.Subject = req.Subject
		retry.OnBehalfOf = req.OnBehalfOf
		retry.IdempotencyKey = req.Key
		retry.CorrelationID = f.RequestID
		if _, err := h.journal.Append(retry, false); err != nil {
			c.error(f, err)
			h.failJournal(err)
			return
		}
		c.result(f, old)
		return
	}
	e := h.event("shim.inject_intent", map[string]any{"key": req.Key, "fingerprint": fingerprint, "mode": req.Mode, "delivery": req.Delivery})
	e.Actor = req.Actor
	e.OnBehalfOf = req.OnBehalfOf
	e.Subject = req.Subject
	e.IdempotencyKey = req.Key
	e.CorrelationID = f.RequestID
	intent, err := h.journal.Append(e, false)
	if err != nil {
		c.error(f, err)
		h.failJournal(err)
		return
	}
	receipt := receipt{Fingerprint: fingerprint, Code: "outcome_unknown", Cursor: intent.Cursor}
	h.receipts[req.Key] = receipt
	executeErr := error(nil)
	switch {
	case req.Generation != fmt.Sprint(h.launch.Generation):
		executeErr = fault("stale_generation", "runtime generation mismatch")
	case req.Mode != "input" && req.Mode != "signal":
		executeErr = fault("unsupported_mode", "stdio has no turn driver")
	case req.Delivery != "immediate":
		executeErr = fault("unsupported_delivery", "stdio has no trustworthy idle/cancel markers")
	default:
		if req.Deadline != "" {
			deadline, e := time.Parse(time.RFC3339Nano, req.Deadline)
			if e != nil {
				executeErr = fault("invalid_request", "invalid deadline")
			} else if !time.Now().Before(deadline) {
				executeErr = fault("expired", "injection deadline passed")
			}
		}
		h.mu.Lock()
		running := h.running
		h.mu.Unlock()
		if !running {
			executeErr = fault("target_offline", "child exited")
		}
		if executeErr == nil && req.Mode == "input" {
			data, e := base64.StdEncoding.DecodeString(req.Data)
			if e != nil || len(data) > OutputChunk {
				executeErr = fault("invalid_request", "input must be bounded base64")
			} else {

				receipt.Bytes, e = h.driver.WriteInput(data)
				if e != nil {
					executeErr = fault("outcome_unknown", "input may be partially written")
				} else {
					receipt.Code = "bytes_written"
				}
			}
		} else if executeErr == nil {
			if req.Signal == 0 {
				executeErr = fault("invalid_request", "signal zero is not an execution signal")
			} else if !allowedSignal(req.Signal) {
				executeErr = fault("unsupported_mode", "signal unsupported")
			} else if e := h.sendSignal(syscall.Signal(req.Signal)); e != nil {
				executeErr = fault("outcome_unknown", "signal result uncertain")
			} else {
				receipt.Code = "signal_sent"
			}
		}
	}
	if executeErr != nil {
		receipt.Code = codeOf(executeErr)
	}
	result := h.event("shim.inject_outcome", map[string]any{"key": req.Key, "receipt": receipt})
	result.Actor = req.Actor
	result.Subject = req.Subject
	result.OnBehalfOf = req.OnBehalfOf
	result.IdempotencyKey = req.Key
	result.CorrelationID = f.RequestID
	outcome, err := h.journal.Append(result, false)
	if err != nil {
		c.error(f, fault("outcome_unknown", "effect outcome could not be committed"))
		h.failJournal(err)
		return
	}
	receipt.Cursor = outcome.Cursor
	h.receipts[req.Key] = receipt
	c.result(f, receipt)
}
func (c *connection) journalRefusal(f Frame, err error) {
	kind, cutType := boundedLogString(f.Type, 128)
	requestID, cutID := boundedLogString(f.RequestID, 256)
	code, cutCode := boundedLogString(codeOf(err), 64)
	event := c.host.event("shim.refused", map[string]string{"request_id": requestID, "type": kind, "code": code})
	event.Truncated = cutType || cutID || cutCode
	_, e := c.host.journal.Append(event, false)
	if e != nil {
		c.error(f, e)
		c.host.failJournal(e)
		return
	}
	c.error(f, err)
}
func allowedSignal(n int) bool {
	return n == int(syscall.SIGTERM) || n == int(syscall.SIGINT) || n == int(syscall.SIGKILL) || n == int(syscall.SIGHUP)
}
func (c *connection) control(f Frame) {
	var req struct {
		Action     string `json:"action"`
		Signal     int    `json:"signal"`
		Generation string `json:"expected_generation"`
	}
	if json.Unmarshal(f.Body, &req) != nil {
		c.journalRefusal(f, fault("invalid_request", "bad control JSON"))
		return
	}
	if req.Generation != strconv.FormatUint(c.host.launch.Generation, 10) {
		c.journalRefusal(f, fault("stale_generation", "runtime generation mismatch"))
		return
	}
	if req.Action != "kill" && req.Action != "signal" {
		c.journalRefusal(f, fault("unsupported_control", "control unavailable for stdio"))
		return
	}
	if req.Action == "signal" && !allowedSignal(req.Signal) {
		c.journalRefusal(f, fault("unsupported_control", "signal unavailable"))
		return
	}
	c.host.mu.Lock()
	running := c.host.running
	c.host.mu.Unlock()
	if !running {
		c.journalRefusal(f, fault("target_offline", "child exited"))
		return
	}
	intent, err := c.host.record("shim.control_intent", map[string]any{"request_id": f.RequestID, "action": req.Action, "signal": req.Signal}, false)
	if err != nil {
		c.error(f, err)
		c.host.failJournal(err)
		return
	}
	if req.Action == "kill" {
		c.host.stop("controller_kill")
	} else {
		err = c.host.sendSignal(syscall.Signal(req.Signal))
	}
	if err != nil {
		c.error(f, fault("outcome_unknown", "control effect uncertain"))
		return
	}
	outcome, err := c.host.record("shim.control_outcome", map[string]string{"request_id": f.RequestID, "intent_cursor": intent.Cursor, "code": "submitted"}, true)
	if err != nil {
		c.error(f, fault("outcome_unknown", "control outcome not committed"))
		c.host.failJournal(err)
		return
	}
	c.result(f, map[string]string{"code": "submitted", "cursor": outcome.Cursor})
}

func (c *connection) dispatch(f Frame) []Frame {
	h := c.host
	h.op.Lock()
	defer h.op.Unlock()
	c.deferResponses = true
	defer func() { c.deferResponses = false; c.pending = nil }()
	h.mu.Lock()
	valid := h.controller == c && h.epoch == c.epoch
	h.mu.Unlock()
	if !valid || f.Epoch != fmt.Sprint(c.epoch) {
		c.journalRefusal(f, fault("stale_controller", "controller epoch displaced"))
		return append([]Frame(nil), c.pending...)
	}
	switch f.Type {
	case "ack":
		c.acknowledge(f)
	case "inject":
		c.inject(f)
	case "control":
		c.control(f)
	default:
		c.error(f, fault("unsupported_message", "unknown command"))
	}
	return append([]Frame(nil), c.pending...)
}

func boundedLogString(value string, limit int) (string, bool) {
	if len(value) > limit {
		return value[:limit], true
	}
	return value, false
}
