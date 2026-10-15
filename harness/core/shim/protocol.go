// Package shim hosts one stdio child independently of its control connection.
// Its journal is private raw execution evidence; downstream redaction and
// authorization belong to the controller.
package shim

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

const (
	ProtocolMajor = 1
	ProtocolMinor = 0
	MaxFrame      = 1 << 20
	OutputChunk   = 64 << 10
)

// Frame uses decimal strings for authority counters. Event envelopes keep the
// shared mesh schema; clients must preserve its uint64 values without rounding.
type Frame struct {
	Major     int             `json:"protocol_major"`
	Minor     int             `json:"protocol_minor"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	Session   string          `json:"session"`
	Epoch     string          `json:"controller_epoch,omitempty"`
	Body      json.RawMessage `json:"body"`
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string         { return e.Code + ": " + e.Message }
func fault(code, message string) error { return &Error{code, message} }
func codeOf(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return "internal_error"
}

func ReadFrame(r io.Reader) (Frame, error) {
	var f Frame
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return f, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > MaxFrame {
		return f, fault("invalid_frame", "frame length outside bounds")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fault("invalid_frame", "invalid JSON frame")
	}
	if f.Type == "" || len(f.Body) == 0 || !json.Valid(f.Body) {
		return f, fault("invalid_frame", "missing message type or body")
	}
	return f, nil
}

func WriteFrame(w io.Writer, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return fault("invalid_frame", "frame too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(b)))
	for _, p := range [][]byte{header[:], b} {
		for len(p) > 0 {
			n, err := w.Write(p)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			p = p[n:]
		}
	}
	return nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Proof binds the capability to a fresh challenge and the selected connection
// role/session. The secret is never sent over the socket or journaled.
func Proof(secret, nonce, session, role string) string {
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "%s\x00%s\x00%s", nonce, session, role)
	return hex.EncodeToString(h.Sum(nil))
}
func body(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
