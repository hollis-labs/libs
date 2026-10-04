package subprocess

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type logMarshalError struct{}

func (logMarshalError) MarshalJSON() ([]byte, error) {
	return nil, errors.New("unregistered-backend-token")
}

type logMarshalPanic struct{}

func (logMarshalPanic) MarshalJSON() ([]byte, error) { panic("unregistered-backend-token") }

type logRegisterMarshaler struct {
	tracker *secretTracker
	secret  string
}

func (v logRegisterMarshaler) MarshalJSON() ([]byte, error) {
	v.tracker.add(v.secret)
	return json.Marshal(map[string]string{"value": v.secret})
}

func readLogRecord(t *testing.T, b *bytes.Buffer) map[string]any {
	t.Helper()
	if b.Len() > maxLogRecordBytes+1 {
		t.Fatalf("oversized log: %d", b.Len())
	}
	if bytes.Count(b.Bytes(), []byte{'\n'}) != 1 {
		t.Fatal("log must be one JSON line")
	}
	var record map[string]any
	d := json.NewDecoder(bytes.NewReader(b.Bytes()))
	d.UseNumber()
	if err := d.Decode(&record); err != nil {
		t.Fatal(err)
	}
	return record
}
func TestLogRedactionPaths(t *testing.T) {
	secret := "secret\"\\\n☃"
	tracker := newSecretTracker()
	config := newConfigReader(map[string]string{"token": secret}, tracker)
	if config.Secret("token") != secret {
		t.Fatal("secret changed")
	}
	var out bytes.Buffer
	logger := &stderrLogger{out: &out, secrets: tracker}
	raw, _ := json.Marshal(map[string]string{"value": secret})
	logger.With("base", map[string]any{"token": secret}).Info("prefix "+secret+" suffix",
		"nested", map[string]any{"items": []any{map[string]string{"token": secret}, struct {
			Token string `json:"token"`
		}{secret}}},
		"raw", json.RawMessage(raw), "bytes", []byte(secret), "encoded", "Bearer "+base64.StdEncoding.EncodeToString([]byte(secret)),
		"large", json.Number("9007199254740993"), "plain", "ordinary value")
	record := readLogRecord(t, &out)
	if record["msg"] != "prefix [REDACTED] suffix" {
		t.Fatalf("message: %v", record["msg"])
	}
	if record["bytes"] != "[REDACTED]" || record["encoded"] != "Bearer [REDACTED]" {
		t.Fatal("encoded secret exposed")
	}
	if record["base"].(map[string]any)["token"] != "[REDACTED]" || record["raw"].(map[string]any)["value"] != "[REDACTED]" {
		t.Fatal("base/raw secret exposed")
	}
	for _, item := range record["nested"].(map[string]any)["items"].([]any) {
		if item.(map[string]any)["token"] != "[REDACTED]" {
			t.Fatal("nested secret exposed")
		}
	}
	if record["large"] != json.Number("9007199254740993") || record["plain"] != "ordinary value" {
		t.Fatal("unrelated values changed")
	}
}
func TestLogRedactionBinaryAndOverlapping(t *testing.T) {
	tracker := newSecretTracker()
	tracker.add("abcdef")
	tracker.add("abc")
	binary := []byte{0xff, 0x00, 0xfe}
	tracker.add(string(binary))
	var out bytes.Buffer
	logger := &stderrLogger{out: &out, secrets: tracker}
	logger.Info("abcdef abc", "binary", binary)
	record := readLogRecord(t, &out)
	if record["msg"] != "[REDACTED] [REDACTED]" || record["binary"] != "[REDACTED]" {
		t.Fatal("overlap/binary redaction failed")
	}
}
func TestLogRedactionFallback(t *testing.T) {
	tracker := newSecretTracker()
	tracker.add("token-value")
	cycle := map[string]any{}
	cycle["self"] = cycle
	cases := map[string]any{"marshal_error": logMarshalError{}, "panic": logMarshalPanic{}, "unsupported": func() {}, "cycle": cycle, "oversized": strings.Repeat("x", maxLogRecordBytes+1), "raw_depth": json.RawMessage(strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130))}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			logger := &stderrLogger{out: &out, secrets: tracker}
			logger.Error("failure token-value", "value", value)
			record := readLogRecord(t, &out)
			if record["msg"] != "failure [REDACTED]" || record["marshal_error"] != "unserializable log fields" {
				t.Fatalf("unsafe fallback: %v", record)
			}
			if strings.Contains(out.String(), "unregistered-backend-token") || strings.Contains(out.String(), "token-value") {
				t.Fatal("fallback leaked diagnostic or secret")
			}
		})
	}
}
func TestLogRedactionBounds(t *testing.T) {
	tracker := newSecretTracker()
	tracker.add("token-value")
	var out bytes.Buffer
	logger := &stderrLogger{out: &out, secrets: tracker}
	logger.Info(strings.Repeat("token-value", maxLogRecordBytes), "ignored", 0)
	record := readLogRecord(t, &out)
	if record["msg"] != "unserializable log record" {
		t.Fatal("oversized message not discarded")
	}
	out.Reset()
	fields := make([]interface{}, 2*(maxLogFields+1))
	for i := 0; i < len(fields); i += 2 {
		fields[i] = "key"
		fields[i+1] = "token-value"
	}
	logger.With(fields...).Info("token-value")
	record = readLogRecord(t, &out)
	if record["msg"] != "[REDACTED]" || record["marshal_error"] == nil {
		t.Fatal("field bound not safe")
	}
	out.Reset()
	logger.Info("token-value", "array", make([]int, maxLogValues+1))
	record = readLogRecord(t, &out)
	if record["msg"] != "[REDACTED]" || record["marshal_error"] == nil {
		t.Fatal("value bound not safe")
	}
	out.Reset()
	tracker.add(strings.Repeat("x", maxLogRecordBytes+1))
	logger.Info("token-value")
	if readLogRecord(t, &out)["msg"] != "[REDACTED]" {
		t.Fatal("large registration blocked redaction")
	}
}
func TestLogRedactionRegistrationDuringSerialization(t *testing.T) {
	tracker := newSecretTracker()
	var out bytes.Buffer
	logger := &stderrLogger{out: &out, secrets: tracker}
	logger.Info("new-token", "object", logRegisterMarshaler{tracker, "new-token"})
	record := readLogRecord(t, &out)
	if record["msg"] != "[REDACTED]" || record["object"].(map[string]any)["value"] != "[REDACTED]" {
		t.Fatal("late registration exposed secret")
	}
}
func TestLogRedactionConcurrentRegistrationAndFallback(t *testing.T) {
	tracker := newSecretTracker()
	var out bytes.Buffer
	logger := &stderrLogger{out: &out, secrets: tracker}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := fmt.Sprintf("concurrent-token-%03d", i)
			tracker.add(v)
			logger.Info(v, "bad", logMarshalError{})
			logger.Info(v, "nested", map[string]string{"secret": v})
		}(i)
	}
	wg.Wait()
	if strings.Contains(out.String(), "concurrent-token-") || strings.Contains(out.String(), "unregistered-backend-token") {
		t.Fatal("concurrent logging exposed secret")
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})
	if len(lines) != 40 {
		t.Fatalf("lost log lines: %d", len(lines))
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatal("interleaved log lines")
		}
	}
}

func TestLogRedactionPatternBudgetFailsClosed(t *testing.T) {
	tracker := newSecretTracker()
	for i := 0; i < maxLogPatterns; i++ {
		tracker.add(fmt.Sprintf("registered-value-%05d", i))
	}
	var out bytes.Buffer
	logger := &stderrLogger{out: &out, secrets: tracker}
	logger.Info("registered-value-00000", "secret", "registered-value-01000")
	record := readLogRecord(t, &out)
	if record["msg"] != "[REDACTED]" || record["marshal_error"] == nil || strings.Contains(out.String(), "registered-value-") {
		t.Fatal("partial redaction snapshot leaked")
	}
}
