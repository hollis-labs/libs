package subprocess

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	plugin "github.com/hollis-labs/plugin-sdk"
)

// stderrLogger is a simple JSON-lines logger that writes to stderr.
// Subprocess plugins must log to stderr because stdout is reserved for
// the JSON-RPC wire protocol — any stray byte on stdout corrupts RPC
// framing and crashes the transport.
type stderrLogger struct {
	mu      sync.Mutex
	out     io.Writer
	kvs     []interface{}
	secrets *secretTracker
}

type logRecord struct {
	Time    string `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"msg"`
	// KVs are flattened into the record at marshal time.
}

// secretTracker records values that have been marked as secrets so that
// the logger can redact any key-value pair whose value matches a known
// secret before writing it to stderr.
type secretTracker struct {
	mu      sync.RWMutex
	secrets map[string]struct{}
}

func newSecretTracker() *secretTracker {
	return &secretTracker{secrets: make(map[string]struct{})}
}

func (s *secretTracker) add(v string) {
	if v == "" {
		return
	}
	s.mu.Lock()
	s.secrets[v] = struct{}{}
	s.mu.Unlock()
}

func (s *secretTracker) isSecret(v string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.secrets[v]
	return ok
}

// packageLogger is the singleton returned by Log(). It is nil-safe
// before Serve has initialized it — pre-init calls are silently dropped
// via a no-op writer.
var (
	pkgLoggerMu sync.RWMutex
	pkgLogger   plugin.Logger = &stderrLogger{out: io.Discard, secrets: newSecretTracker()}
)

// Log returns the package-level logger. It is safe to call before
// Serve() has initialized logging; pre-init calls write to a discard
// sink rather than panicking.
func Log() plugin.Logger {
	pkgLoggerMu.RLock()
	defer pkgLoggerMu.RUnlock()
	return pkgLogger
}

// setPackageLogger installs the given logger as the Log() return value.
// Called by Serve during init.
func setPackageLogger(l plugin.Logger) {
	pkgLoggerMu.Lock()
	pkgLogger = l
	pkgLoggerMu.Unlock()
}

// newStderrLogger constructs a stderr JSON-lines logger backed by the
// given secret tracker (so config.Secret lookups feed into redaction).
func newStderrLogger(secrets *secretTracker) *stderrLogger {
	return &stderrLogger{out: os.Stderr, secrets: secrets}
}

func (l *stderrLogger) Debug(msg string, kv ...interface{}) { l.log("debug", msg, kv) }
func (l *stderrLogger) Info(msg string, kv ...interface{})  { l.log("info", msg, kv) }
func (l *stderrLogger) Warn(msg string, kv ...interface{})  { l.log("warn", msg, kv) }
func (l *stderrLogger) Error(msg string, kv ...interface{}) { l.log("error", msg, kv) }

// With returns a logger that prepends the given key-value pairs to
// every record. Implementations share the underlying writer + secret
// tracker; only the accumulated kvs diverge.
func (l *stderrLogger) With(kv ...interface{}) plugin.Logger {
	child := &stderrLogger{
		out:     l.out,
		secrets: l.secrets,
		kvs:     append(append([]interface{}{}, l.kvs...), kv...),
	}
	return child
}

func (l *stderrLogger) log(level, msg string, kv []interface{}) {
	if l == nil || l.out == nil {
		return
	}

	// Flatten base kvs + per-call kvs into an ordered map so JSON
	// preserves the arguments' original order.
	flat := map[string]interface{}{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": level,
		"msg":   msg,
	}
	flat = mergeKVs(flat, l.kvs, l.secrets)
	flat = mergeKVs(flat, kv, l.secrets)

	data, err := json.Marshal(flat)
	if err != nil {
		// As a fallback, write a plain line so we don't swallow events
		// silently. Still goes to stderr.
		_, _ = fmt.Fprintf(l.out, "{\"level\":%q,\"msg\":%q,\"marshal_error\":%q}\n", level, msg, err.Error())
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(append(data, '\n'))
}

// mergeKVs copies pairs from kv into dst, redacting string values the
// secret tracker knows about. Odd-length slices are ignored past the
// last complete pair.
func mergeKVs(dst map[string]interface{}, kv []interface{}, secrets *secretTracker) map[string]interface{} {
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			key = fmt.Sprintf("%v", kv[i])
		}
		val := kv[i+1]
		if s, ok := val.(string); ok && secrets != nil && secrets.isSecret(s) {
			val = "[REDACTED]"
		}
		dst[key] = val
	}
	return dst
}
