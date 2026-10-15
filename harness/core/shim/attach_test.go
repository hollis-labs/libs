package shim

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClosingDuringDurableAttach(t *testing.T) {
	for _, role := range []string{"controller", "observer"} {
		t.Run(role, func(t *testing.T) {
			spec := launchTest(t, "echo")
			spec.Heartbeat = 30 * time.Millisecond
			h, err := Start(spec)
			if err != nil {
				t.Fatal(err)
			}
			awaitKind(t, h, "shim.output")
			entered, release := make(chan struct{}), make(chan struct{})
			var gate, releaseOnce sync.Once
			h.journal.mu.Lock()
			syncFile := h.journal.syncFile
			h.journal.syncFile = func(f *os.File) error { gate.Do(func() { close(entered); <-release }); return syncFile(f) }
			h.journal.mu.Unlock()
			type connected struct {
				client *Client
				err    error
			}
			result := make(chan connected, 1)
			go func() {
				c, e := Connect(h.SocketPath(), spec.Secret, spec.Session, spec.Instance, "1", role, false)
				result <- connected{c, e}
			}()
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				select {
				case r := <-result:
					if r.client != nil {
						r.client.Close()
					}
				case <-time.After(time.Second):
				}
				h.Close()
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("attach did not reach durable-record gate")
			}
			closed := make(chan struct{})
			go func() { h.Close(); close(closed) }()
			deadline := time.Now().Add(time.Second)
			swept := false
			for time.Now().Before(deadline) {
				h.mu.Lock()
				swept = h.cause == "host_shutdown"
				h.mu.Unlock()
				if swept {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !swept {
				t.Fatal("close did not sweep connections")
			}
			releaseOnce.Do(func() { close(release) })
			// Keep a successfully attached client responsive until Close returns. The
			// old code registers it after the sweep and then waits forever for it.
			select {
			case <-closed:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("late attach escaped Close connection sweep")
			}
		})
	}
}

func TestRefusalBoundsUntrustedType(t *testing.T) {
	h, spec := hostTest(t, "echo")
	c := clientTest(t, h, spec, "observer")
	for i := 0; i < 40; i++ {
		if err := c.Send(spec.Session, strings.Repeat("x", 500000), map[string]bool{"invalid": true}); err != nil {
			t.Fatal(err)
		}
		f := receive(t, c, "error")
		var b struct {
			Code string `json:"code"`
		}
		json.Unmarshal(f.Body, &b)
		if b.Code != "read_only" {
			t.Fatal(b.Code)
		}
	}
	refused := 0
	for _, e := range h.journal.Snapshot() {
		if e.Kind == "shim.refused" {
			refused++
			if len(e.Payload) > 1024 || !e.Truncated {
				t.Fatalf("unbounded refusal: %d bytes, truncated=%v", len(e.Payload), e.Truncated)
			}
		}
	}
	if refused != 40 {
		t.Fatalf("lost refusal evidence: %d", refused)
	}
	select {
	case <-h.Done():
		t.Fatal("oversized refusal metadata killed child")
	default:
	}
}
