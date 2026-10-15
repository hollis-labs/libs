package subprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Bounds apply to SDK work; author MarshalJSON implementations own their work.
const maxLogRecordBytes = 64 << 10
const maxLogFields = 128
const maxLogValues = 4096
const maxLogPatterns = 1024

var errLogRecord = errors.New("unserializable log record")

// No second secret store: the bounded snapshot is used for one log operation.
// If it cannot cover every matching pattern, mask all strings rather than leak.
func (s *secretTracker) redactor() (func(string) string, bool) {
	if s == nil {
		return func(v string) string { return v }, false
	}
	s.mu.RLock()
	patterns := make([]string, 0)
	patternBytes := 0
	visited := 0
	for pattern := range s.secrets {
		visited++
		if visited > maxLogPatterns {
			s.mu.RUnlock()
			return func(string) string { return "[REDACTED]" }, true
		}
		if len(pattern) > maxLogRecordBytes {
			continue // cannot occur in a bounded record
		}
		patternBytes += len(pattern)
		if len(patterns) == maxLogPatterns || patternBytes > maxLogRecordBytes {
			s.mu.RUnlock()
			return func(string) string { return "[REDACTED]" }, true
		}
		patterns = append(patterns, pattern)
	}
	s.mu.RUnlock()
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) == len(patterns[j]) {
			return patterns[i] < patterns[j]
		}
		return len(patterns[i]) > len(patterns[j])
	})
	pairs := make([]string, 0, 2*len(patterns))
	for _, pattern := range patterns {
		pairs = append(pairs, pattern, "[REDACTED]")
	}
	replacer := strings.NewReplacer(pairs...)
	return func(v string) string {
		if len(v) > maxLogRecordBytes {
			return "[REDACTED]"
		}
		e := frameEncoder{limit: maxLogRecordBytes}
		if _, err := replacer.WriteString(&e, v); err != nil {
			return "[REDACTED]"
		}
		return string(e.data)
	}, false
}

func stageLogRecord(flat map[string]interface{}, base, kv []interface{}, invalid bool) (data []byte, err error) {
	err = errLogRecord
	defer func() {
		if recover() != nil {
			data, err = nil, errLogRecord
		}
	}()
	if invalid || len(base)+len(kv) > 2*maxLogFields {
		return nil, errLogRecord
	}
	if err = mergeKVs(flat, base); err != nil {
		return nil, err
	}
	if err = mergeKVs(flat, kv); err != nil {
		return nil, err
	}
	return marshalBounded(flat, maxLogRecordBytes)
}

// Work on bounded encoded JSON, including RawMessage/MarshalJSON and []byte
// representations. UseNumber preserves integer precision and number tokens.
func redactLogJSON(raw []byte, redact func(string) string) ([]byte, error) {
	if len(raw) > maxLogRecordBytes {
		return nil, errLogRecord
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errLogRecord
	}
	remaining := maxLogValues
	var walk func(any, int) (any, error)
	walk = func(v any, depth int) (any, error) {
		remaining--
		if remaining < 0 || depth > 128 {
			return nil, errLogRecord
		}
		switch v := v.(type) {
		case string:
			return redact(v), nil
		case map[string]any:
			out := make(map[string]any, len(v))
			for key, item := range v {
				child, err := walk(item, depth+1)
				if err != nil {
					return nil, err
				}
				out[redact(key)] = child
			}
			return out, nil
		case []any:
			for i, item := range v {
				child, err := walk(item, depth+1)
				if err != nil {
					return nil, err
				}
				v[i] = child
			}
		}
		return v, nil
	}
	value, err := walk(value, 0)
	if err != nil {
		return nil, err
	}
	return marshalBounded(value, maxLogRecordBytes)
}

func safeLogFallback(level, msg string, redact func(string) string) []byte {
	// Oversized/invalid message text cannot compromise fallback bounds. Never
	// include an author's marshal error, formatting output or rejected fields.
	if len(msg) > maxLogRecordBytes/4 {
		msg = "unserializable log record"
	}
	value := map[string]string{"level": level, "msg": redact(msg), "marshal_error": "unserializable log fields"}
	if data, err := marshalBounded(value, maxLogRecordBytes); err == nil {
		return data
	}
	return []byte(`{"level":"error","msg":"unserializable log record","marshal_error":"unserializable log fields"}`)
}
