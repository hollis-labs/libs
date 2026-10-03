package ssekit_test

import (
	"bytes"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/ui-go/ssekit"
)

// ownImportPath is this package's import path, read from a type it exports instead of
// written out. A goroutine started by this module shows it in its stack frames. It used
// to be a literal naming the old standalone repository; when the code moved into the
// ui-go module that literal matched nothing, and every leak check in this package passed
// without checking anything.
var ownImportPath = reflect.TypeOf(ssekit.Event{}).PkgPath()

// noLeaks fails the test if goroutines started by this module are still alive
// shortly after the test's own cleanups have run. Register it first: cleanups
// run last-in first-out.
func noLeaks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for {
			leaked := ourGoroutines()
			if len(leaked) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("goroutines leaked:\n%s", strings.Join(leaked, "\n---\n"))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func ourGoroutines() []string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if !strings.Contains(g, ownImportPath) {
			continue
		}
		// The goroutine running the test itself and the testing framework are
		// not leaks.
		if strings.Contains(g, "testing.tRunner") || strings.Contains(g, "testing.(*T).Run") ||
			strings.Contains(g, "testing.runFuzz") || strings.Contains(g, "ourGoroutines") {
			continue
		}
		out = append(out, g)
	}
	return out
}

// memWriter is an in-memory http.ResponseWriter that can flush and can be told
// to fail. It is safe for use from several goroutines.
type memWriter struct {
	mu        sync.Mutex
	hdr       http.Header
	status    int
	buf       bytes.Buffer
	flushes   int
	writeErr  error // returned by Write once failAfter writes have succeeded
	failAfter int   // number of writes that succeed before writeErr; <0 never
	flushErr  error
}

func newMemWriter() *memWriter { return &memWriter{hdr: http.Header{}, failAfter: -1} }

func (m *memWriter) Header() http.Header { return m.hdr }

func (m *memWriter) WriteHeader(code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status == 0 {
		m.status = code
	}
}

func (m *memWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAfter == 0 {
		return 0, m.writeErr
	}
	if m.failAfter > 0 {
		m.failAfter--
	}
	return m.buf.Write(p)
}

func (m *memWriter) Flush() { m.mu.Lock(); m.flushes++; m.mu.Unlock() }

func (m *memWriter) FlushError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flushErr != nil {
		return m.flushErr
	}
	m.flushes++
	return nil
}

func (m *memWriter) Bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return bytes.Clone(m.buf.Bytes())
}

func (m *memWriter) String() string { return string(m.Bytes()) }

// noFlushWriter is a ResponseWriter with no way to flush.
type noFlushWriter struct{ hdr http.Header }

func (n *noFlushWriter) Header() http.Header         { return n.hdr }
func (n *noFlushWriter) Write(p []byte) (int, error) { return len(p), nil }
func (n *noFlushWriter) WriteHeader(int)             {}
