package subprocess

import (
	"encoding/base64"
	"io"
	"os"
	"sync"
	"time"

	plugin "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk"
)

// stderrLogger is a simple JSON-lines logger that writes to stderr.
// Subprocess plugins must log to stderr because stdout is reserved for
// the JSON-RPC wire protocol — any stray byte on stdout corrupts RPC
// framing and crashes the transport.
type stderrLogger struct {
	mu      sync.Mutex
	out     io.Writer
	kvs     []interface{}
	invalid bool
	secrets *secretTracker
}

type logRecord struct {
	Time    string `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"msg"`
	// KVs are flattened into the record at marshal time.
}

// secretTracker records values that have been marked as secrets so that
// SDK logging can redact message text and serialized structured values.
// It does not intercept writes made directly to stderr.
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
	// Encoded bytes are strings in JSON. Keep representations in this same
	// tracker, and avoid allocating an alias too large to fit a log record.
	if len(v) <= 3*(maxLogRecordBytes/4) {
		s.secrets[base64.StdEncoding.EncodeToString([]byte(v))] = struct{}{}
	}
	s.mu.Unlock()
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
	child := &stderrLogger{out: l.out, secrets: l.secrets, invalid: l.invalid}
	if len(l.kvs)+len(kv) > 2*maxLogFields {
		child.invalid = true
	} else {
		child.kvs = append(append([]interface{}{}, l.kvs...), kv...)
	}
	return child
}

func (l *stderrLogger) log(level, msg string, kv []interface{}) {
	if l == nil || l.out == nil {
		return
	}
	flat := map[string]interface{}{"ts": time.Now().UTC().Format(time.RFC3339Nano), "level": level, "msg": msg}
	data, err := stageLogRecord(flat, l.kvs, kv, l.invalid)
	// Snapshot after user serialization: a custom marshaler may register a
	// secret, and none of its error text is safe for the fallback.
	redact, maskAll := l.secrets.redactor()
	if maskAll {
		err = errLogRecord
	}
	if err == nil {
		data, err = redactLogJSON(data, redact)
	}
	if err != nil {
		data = safeLogFallback(level, msg, redact)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(append(data, '\n'))
}

// mergeKVs accepts string keys and ignores an unmatched trailing argument.
// Other keys must not invoke unbounded author formatting in the fallback.
func mergeKVs(dst map[string]interface{}, kv []interface{}) error {
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			return errLogRecord
		}
		dst[key] = kv[i+1]
	}
	return nil
}
