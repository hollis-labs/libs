package shim

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client owns only a connection. Closing or reconnecting it never stops the
// hosted process. Consumers persist each Event transactionally before Ack.
// Responses and replay events share Frames in wire order.
type Client struct {
	socket  *net.UnixConn
	Epoch   string
	Journal string
	Hello   json.RawMessage
	Frames  <-chan Frame
	frames  chan Frame
	done    chan struct{}
	writeMu sync.Mutex
	once    sync.Once
}

func Connect(path, secret, session, instance, generation, role string, takeover bool) (*Client, error) {
	socket, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			socket.Close()
		}
	}()
	socket.SetDeadline(time.Now().Add(5 * time.Second))
	challenge, err := ReadFrame(socket)
	if err != nil {
		return nil, err
	}
	var b struct {
		Nonce string `json:"nonce"`
		Epoch string `json:"controller_epoch"`
	}
	if challenge.Type != "hello" || challenge.Session != session || json.Unmarshal(challenge.Body, &b) != nil || b.Nonce == "" {
		return nil, fault("unauthorized", "invalid server challenge")
	}
	hello := Hello{Major: ProtocolMajor, Minor: ProtocolMinor, Role: role, Proof: Proof(secret, b.Nonce, session, role), Instance: instance, Generation: generation, Epoch: b.Epoch, Takeover: takeover}
	if err = WriteFrame(socket, Frame{Major: ProtocolMajor, Minor: ProtocolMinor, Type: "hello", RequestID: newID(), Session: session, Body: body(hello)}); err != nil {
		return nil, err
	}
	response, err := ReadFrame(socket)
	if err != nil {
		return nil, err
	}
	if response.Type == "error" {
		var result struct {
			Code string `json:"code"`
		}
		json.Unmarshal(response.Body, &result)
		return nil, fault(result.Code, "hello refused")
	}
	var ready struct {
		Epoch   string `json:"controller_epoch"`
		Journal string `json:"journal"`
	}
	if response.Type != "hello" || json.Unmarshal(response.Body, &ready) != nil || ready.Journal == "" {
		return nil, fault("invalid_frame", "invalid hello response")
	}
	socket.SetDeadline(time.Time{})
	c := &Client{socket: socket, Epoch: ready.Epoch, Journal: ready.Journal, Hello: response.Body, frames: make(chan Frame, 256), done: make(chan struct{})}
	c.Frames = c.frames
	go c.read(session)
	ok = true
	return c, nil
}
func (c *Client) read(session string) {
	defer close(c.frames)
	defer c.Close()
	for {
		f, err := ReadFrame(c.socket)
		if err != nil {
			return
		}
		if f.Type == "health" {
			if err = c.Send(session, "health", map[string]bool{"pong": true}); err != nil {
				return
			}
			continue
		}
		select {
		case c.frames <- f:
		case <-c.done:
			return
		}
	}
}
func (c *Client) Send(session, kind string, v any) error {
	return c.SendFrame(Frame{Major: ProtocolMajor, Minor: ProtocolMinor, Type: kind, RequestID: newID(), Session: session, Epoch: c.Epoch, Body: body(v)})
}
func (c *Client) SendFrame(f Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.socket.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return WriteFrame(c.socket, f)
}
func (c *Client) Replay(session, after string) error {
	return c.Send(session, "replay", map[string]string{"after_cursor": after})
}
func (c *Client) Ack(session, cursor string) error {
	return c.Send(session, "ack", map[string]string{"cursor": cursor})
}
func (c *Client) Close() error {
	var err error
	c.once.Do(func() { close(c.done); err = c.socket.Close() })
	return err
}
func (c *Client) String() string {
	return fmt.Sprintf("shim client journal=%s epoch=%s", c.Journal, c.Epoch)
}
