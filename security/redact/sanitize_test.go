package redact

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func sanitizerFor(t testing.TB, p Policy, opts ...Option) *Sanitizer {
	t.Helper()
	s, err := NewSanitizer(p, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func jsonString(s string) []byte { b, _ := json.Marshal(s); return b }

func TestSanitizePII(t *testing.T) {
	// All values are synthetic. Credentials are assembled, never stored as keys.
	cases := []struct {
		name, value string
		want        bool
	}{
		{"email", "reader@example.test", true},
		{"phone international", "+44 20 7946 0123", true},
		{"phone national", "(202) 555-0123", true},
		{"ipv4", "192.0.2.12", true},
		{"ipv6", "2001:db8::1", true},
		{"ipv6 bracketed", "[2001:db8::1]:443", true},
		{"ipv6 mapped", "::ffff:192.0.2.12", true},
		{"ipv6 zone", "fe80::1%example", true},
		{"card", strings.Repeat("4", 1) + strings.Repeat("1", 15), true},
		{"card spaced", "4111 1111 1111 1111", true},
		{"card bad checksum", "4111 1111 1111 1112", false},
		{"card too long", "1" + strings.Repeat("4", 1) + strings.Repeat("1", 15) + "123", false},
		{"card all zero", strings.Repeat("0", 16), false},
		{"nanite id", strings.Repeat("ab", 16), true},
		{"hex substring", strings.Repeat("ab", 17), false},
		{"not an ip", "999.999.999.999", false},
		{"timestamp", "2026-10-16T12:00:00Z", false},
		{"ordinary", "worker ready", false},
	}
	s := sanitizerFor(t, Policy{Mode: Denylist})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, r := s.Sanitize(jsonString(tc.value))
			if r.ErrorClass != "" || r.PII != tc.want {
				t.Fatalf("classification mismatch: %+v", r)
			}
			if tc.want {
				if bytes.Contains(out, []byte(tc.value)) || r.Redactions == 0 || r.Sensitivity != Private {
					t.Fatal("PII was not redacted and classified private")
				}
				again, rr := s.Sanitize(out)
				if !bytes.Equal(again, out) || rr.Redactions != 0 {
					t.Fatal("redaction is not idempotent")
				}
			} else if !bytes.Equal(out, jsonString(tc.value)) {
				t.Fatal("benign input changed")
			}
		})
	}
}

func TestSanitizeEventCorpus(t *testing.T) {
	// Payload shapes follow the runtimeevents state_changed, approval.requested,
	// and alert schema examples. Added sensitive values exercise real field paths.
	key := "gh" + "p_" + strings.Repeat("Q", 32)
	email := "reader@example.test"
	id := strings.Repeat("a1", 16)
	cases := []struct {
		name      string
		payload   map[string]any
		policy    Policy
		wantPII   bool
		wantClass Sensitivity
	}{
		{"run.state_changed", map[string]any{"entity": map[string]any{"type": "workflow_run", "id": id}, "from": "queued", "to": "running", "trigger": map[string]any{"kind": "agent", "note": email}}, Policy{Mode: Denylist}, true, Private},
		{"approval.requested", map[string]any{"request_id": "request-1", "hitl_ref": "msg://approval/cerberus/request-1", "reason": "Authorization: Bearer " + key, "requester": email}, Policy{Mode: Denylist}, true, Secret},
		{"alert.raised", map[string]any{"rule_id": "rule-1", "source_event_ids": []string{"event-1"}, "summary": "endpoint 192.0.2.12 failed", "details": map[string]any{"access_token": key}}, Policy{AllowedPaths: []string{"/rule_id", "/source_event_ids/*", "/summary"}}, true, Secret},
		{"credential object", map[string]any{"credentials": map[string]any{"login": email, "value": key}, "status": "ready"}, Policy{Mode: Denylist}, true, Secret},
		{"overlap", map[string]any{"message": "password=" + email}, Policy{Mode: Denylist}, true, Secret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sanitizerFor(t, tc.policy)
			input, _ := json.Marshal(tc.payload)
			out, r := s.Sanitize(input)
			if !json.Valid(out) || r.ErrorClass != "" || r.Redactions == 0 || r.PII != tc.wantPII || r.Sensitivity != tc.wantClass {
				t.Fatalf("bad report: %+v", r)
			}
			audit, _ := json.Marshal(r)
			for _, seed := range []string{key, email, id} {
				if bytes.Contains(out, []byte(seed)) || bytes.Contains(audit, []byte(seed)) {
					t.Fatal("seed escaped payload/report")
				}
			}
			again, r2 := s.Sanitize(out)
			if !bytes.Equal(out, again) || r2.Redactions != 0 {
				t.Fatal("corpus output not idempotent")
			}
		})
	}
}

func TestSanitizeAllowlist(t *testing.T) {
	s := sanitizerFor(t, Policy{AllowedPaths: []string{"/rows/*/name", "/rows/0/status", "/a~1b/~0x", "/password"}})
	input := []byte(`{"rows":[{"name":"one","status":"ok","extra":"drop"},{"name":"two","status":"drop"}],"a/b":{"~x":42},"password":"synthetic","unknown":{"nested":"drop"}}`)
	out, r := s.Sanitize(input)
	want := `{"rows":[{"name":"one","status":"ok"},{"name":"two"}],"a/b":{"~x":42},"password":"[REDACTED]"}`
	if string(out) != want || r.Redactions != 4 || r.Sensitivity != Secret {
		t.Fatalf("unexpected allowlist result/report: %+v", r)
	}
	if !reflect.DeepEqual(r.Paths, []string{"/rows/0/*", "/rows/1/*", "/password", "/*"}) {
		t.Fatal("incorrect JSON pointers")
	}
	// Empty policy removes all undeclared members. Parent path is not a subtree grant.
	for _, p := range []Policy{{}, {AllowedPaths: []string{"/rows"}}} {
		s := sanitizerFor(t, p)
		out, _ := s.Sanitize(input)
		if bytes.Contains(out, []byte("one")) || bytes.Contains(out, []byte("synthetic")) {
			t.Fatal("allowlist retained an undeclared value")
		}
	}
	a := sanitizerFor(t, Policy{AllowedPaths: []string{"/1"}})
	out, _ = a.Sanitize([]byte(`["drop","keep"]`))
	if string(out) != `[null,"keep"]` {
		t.Fatal("array indices shifted")
	}
	again, r2 := a.Sanitize(out)
	if !bytes.Equal(out, again) || r2.Redactions != 0 {
		t.Fatal("array removal not idempotent")
	}
}

func TestSanitizeSecretKeysAndNumbers(t *testing.T) {
	s := sanitizerFor(t, Policy{Mode: Denylist})
	input := []byte(`{"reader@example.test":"value","count":42,"number":4111111111111111,"nested":{"token":"example-` + `value"}}`)
	out, r := s.Sanitize(input)
	audit, _ := json.Marshal(r)
	if bytes.Contains(out, []byte("reader@")) || bytes.Contains(audit, []byte("reader@")) {
		t.Fatal("key leaked in payload/report")
	}
	if len(r.Paths) == 0 || r.Paths[0] != "/*" {
		t.Fatal("sensitive key path is not masked")
	}
	if !r.PII || r.Sensitivity != Secret || r.Redactions != 3 {
		t.Fatalf("unexpected classifications: %+v", r)
	}
	if !bytes.Contains(out, []byte(`"count":42`)) || bytes.Contains(out, []byte("411111")) {
		t.Fatal("numeric redaction failed")
	}
}

func TestSanitizeFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		p     Policy
		input []byte
		class ErrorClass
	}{
		{"oversized", Policy{MaxBytes: 2}, []byte(`"long"`), ErrorOversized},
		{"default cap", Policy{}, bytes.Repeat([]byte("x"), DefaultMaxBytes+1), ErrorOversized},
		{"invalid", Policy{}, []byte(`{"secret":`), ErrorInvalidJSON},
		{"trailing", Policy{}, []byte(`{} {}`), ErrorInvalidJSON},
		{"duplicate", Policy{}, []byte(`{"a":1,"a":2}`), ErrorInvalidJSON},
		{"duplicate escaped", Policy{}, []byte(`{"a":1,"\u0061":2}`), ErrorInvalidJSON},
		{"utf8", Policy{}, []byte{'"', 0xff, '"'}, ErrorInvalidJSON},
		{"opaque", Policy{Format: Opaque}, []byte(`"value"`), ErrorOpaque},
		{"depth", Policy{}, []byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)), ErrorDepth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, r := sanitizerFor(t, tc.p).Sanitize(tc.input)
			if r.ErrorClass != tc.class || string(out) != `{"sanitize_error":"`+string(tc.class)+`"}` || r.Sensitivity != Secret || len(r.Paths) != 0 {
				t.Fatal("failure did not return a safe tombstone")
			}
		})
	}
	for _, p := range []Policy{{Mode: 99}, {Format: 99}, {MaxBytes: -1}, {Sensitivity: "unknown"}, {AllowedPaths: []string{"not/pointer"}}, {AllowedPaths: []string{"/~2"}}} {
		if _, err := NewSanitizer(p); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestSanitizePolicyAndConcurrentUse(t *testing.T) {
	paths := []string{"/message"}
	s := sanitizerFor(t, Policy{AllowedPaths: paths, Sensitivity: Secret})
	paths[0] = "/unsafe"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 50; j++ {
				out, r := s.Sanitize([]byte(`{"message":"reader@example.test","unsafe":"drop"}`))
				if string(out) != `{"message":"[REDACTED]"}` || r.Sensitivity != Secret || !r.PII {
					t.Error("policy mutable or concurrent result incorrect")
					return
				}
			}
		})
	}
	wg.Wait()
}

func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{`{}`, `{"message":"reader@example.test"}`, `[1,null,"safe"]`, `{"x":{"token":"example-` + `value"}}`} {
		f.Add([]byte(seed))
	}
	s, _ := NewSanitizer(Policy{Mode: Denylist})
	f.Fuzz(func(t *testing.T, input []byte) {
		out, r := s.Sanitize(input)
		if !json.Valid(out) {
			t.Fatal("output invalid JSON")
		}
		if r.ErrorClass == "" {
			again, r2 := s.Sanitize(out)
			if !bytes.Equal(out, again) || r2.ErrorClass != "" || r2.Redactions != 0 {
				t.Fatal("successful output is not idempotent")
			}
		}
	})
}

func benchmarkPayload() []byte {
	// 4 KiB event payload with an embedded email, provider credential, IP and
	// otherwise ordinary prose. No fixture credentials are stored verbatim.
	key := "gh" + "p_" + strings.Repeat("Q", 32)
	base := `{"entity":{"type":"workflow_run","id":"run-1"},"from":"queued","to":"running","note":"reader@example.test 192.0.2.12 ` + key + ` `
	return []byte(base + strings.Repeat("ordinary worker progress ", (4096-len(base)-2)/25) + strings.Repeat("x", (4096-len(base)-2)%25) + `"}`)
}
func BenchmarkSanitize4KB(b *testing.B) {
	s := sanitizerFor(b, Policy{Mode: Denylist})
	payload := benchmarkPayload()
	if len(payload) != 4096 {
		b.Fatalf("benchmark payload size %d", len(payload))
	}
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	// Bounded sample storage; p99 is latency per call, not an aggregate average.
	samples := make([]int64, 0, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, r := s.Sanitize(payload)
		elapsed := time.Since(start).Nanoseconds()
		if r.ErrorClass != "" || !r.PII {
			b.Fatal("invalid benchmark result")
		}
		if len(samples) < cap(samples) {
			samples = append(samples, elapsed)
		}
	}
	b.StopTimer()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	if len(samples) > 0 {
		b.ReportMetric(float64(samples[(len(samples)-1)*99/100]), "p99-ns/op")
	}
}

func TestProviderCandidateScanPreservesRanges(t *testing.T) {
	fixtures := []string{
		"gh" + "p_" + strings.Repeat("Q", 32),
		"sk" + "-proj-" + strings.Repeat("K", 30),
		"SG" + "." + strings.Repeat("a", 20) + "." + strings.Repeat("b", 20),
		"sk-" + strings.Repeat("b", 20) + "_" + "eyJ" + strings.Repeat("c", 8) + "." + strings.Repeat("d", 8) + ".",
		"eyJ" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 12) + ".",
	}
	for _, key := range fixtures {
		for _, input := range []string{key, "prefix " + key + " suffix", "x" + key, "_" + key, "(" + key + ")", key + "_" + key, key + "," + key} {
			var got, want []Finding
			findProviderKey(Default(), newScanText(input), func(a, b int) { got = append(got, Finding{RuleProviderKey, a, b}) })
			for _, m := range providerKeyRe.FindAllStringSubmatchIndex(input, -1) {
				want = append(want, Finding{RuleProviderKey, m[2], m[3]})
			}
			if !reflect.DeepEqual(merge(got), merge(want)) {
				t.Fatal("candidate scan changed reference ranges")
			}
		}
	}
}

func TestSanitizeCorrelationMetadata(t *testing.T) {
	trace := strings.Repeat("a1", 16)
	s := sanitizerFor(t, Policy{Mode: Denylist})
	for _, key := range []string{"trace_id", "span_id", "traceparent", "payload_digest", "source_hash"} {
		payload, _ := json.Marshal(map[string]any{key: trace, "event_id": "evt_" + trace, "sha256": strings.Repeat("a1", 32)})
		out, r := s.Sanitize(payload)
		if !bytes.Contains(out, []byte(trace)) || r.PII || r.Redactions != 0 {
			t.Fatalf("correlation metadata scrubbed under %s", key)
		}
		// The exception never disables other PII or credential detectors.
		for _, value := range []string{"reader@example.test", "gh" + "p_" + strings.Repeat("Q", 32)} {
			payload, _ = json.Marshal(map[string]any{key: value})
			out, r = s.Sanitize(payload)
			if bytes.Contains(out, []byte(value)) || r.Redactions == 0 {
				t.Fatalf("other detector disabled under %s", key)
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{"tool_call": map[string]any{"trace_id": trace}, "customer_id": trace})
	out, r := s.Sanitize(payload)
	if !r.PII || r.Redactions != 1 || !bytes.Contains(out, []byte(`"trace_id":"`+trace+`"`)) {
		t.Fatal("Nanite rule exception leaked to unrelated field")
	}
	// Dropping a container must not manufacture a PII hit from its trace metadata.
	drop := sanitizerFor(t, Policy{})
	payload, _ = json.Marshal(map[string]any{"tool_call": map[string]any{"trace_id": trace}})
	_, r = drop.Sanitize(payload)
	if r.PII {
		t.Fatal("dropped correlation metadata classified PII")
	}
}

func TestSanitizeSecurityReviewProbes(t *testing.T) {
	t.Run("normalized and joined keys", func(t *testing.T) {
		s := sanitizerFor(t, Policy{Mode: Denylist})
		for _, key := range []string{"pass\u200bword", "ｐａｓｓｗｏｒｄ", "𝐩𝐚𝐬𝐬𝐰𝐨𝐫𝐝", "pass_word", "api_key", "apikey", "api-key"} {
			value := "synthetic-" + "fixture-value"
			payload, _ := json.Marshal(map[string]string{key: value})
			out, r := s.Sanitize(payload)
			if bytes.Contains(out, []byte(value)) || r.Sensitivity != Secret || r.Redactions != 1 {
				t.Fatal("normalized/joined secret key evaded policy")
			}
		}
	})
	t.Run("private dropped key never reported", func(t *testing.T) {
		for _, mode := range []Mode{Allowlist, Denylist} {
			s := sanitizerFor(t, Policy{Mode: mode, AllowedPaths: []string{"/msg"}})
			input := []byte(`{"msg":"hi","John Smith private notes":"reader@example.test"}`)
			_, r := s.Sanitize(input)
			audit, _ := json.Marshal(r)
			if bytes.Contains(audit, []byte("John Smith")) || !reflect.DeepEqual(r.Paths, []string{"/*"}) {
				t.Fatal("unapproved field name leaked into audit report")
			}
		}
	})
	t.Run("benign drop stays public", func(t *testing.T) {
		s := sanitizerFor(t, Policy{AllowedPaths: []string{"/msg"}})
		out, r := s.Sanitize([]byte(`{"msg":"hello","extra":"benign text"}`))
		if string(out) != `{"msg":"hello"}` || r.Sensitivity != Public || r.Redactions != 1 || r.PII {
			t.Fatalf("benign drop misclassified: %+v", r)
		}
	})
	for _, value := range []string{"peer 2001:db8::1.", "call 202-555-0123 202-555-0145", "john.smith%40example.com"} {
		s := sanitizerFor(t, Policy{Mode: Denylist})
		out, r := s.Sanitize(jsonString(value))
		if bytes.Contains(out, []byte(value)) || !r.PII || r.Sensitivity != Private || r.Redactions == 0 {
			t.Fatal("PII boundary/encoding probe escaped")
		}
		if strings.HasPrefix(value, "call") && r.Redactions != 2 {
			t.Fatal("adjacent phone numbers not independently scrubbed")
		}
		// Both reviewers found that a missed detector also exposed a dynamic key.
		payload, _ := json.Marshal(map[string]string{value: "x"})
		out, r = sanitizerFor(t, Policy{}).Sanitize(payload)
		audit, _ := json.Marshal(r)
		if string(out) != "{}" || bytes.Contains(audit, []byte(value)) || !r.PII || !reflect.DeepEqual(r.Paths, []string{"/*"}) {
			t.Fatal("PII key or audit path escaped")
		}
	}
}

func FuzzSanitizeAllowlist(f *testing.F) {
	f.Add("private field name", "private-value-eight", "/msg")
	f.Add("pass\u200bword", "reader@example.test", "/rows/*/msg")
	f.Add("a/b~c", strings.Repeat("a1", 16), "/~2")
	f.Fuzz(func(t *testing.T, key, value, path string) {
		// Exercise pointer validation independently; malformed policies must return
		// a value-free error. A fixed schema makes noninterference testable.
		_, _ = NewSanitizer(Policy{AllowedPaths: []string{path}})
		if key == "msg" || !utf8.ValidString(key) || !utf8.ValidString(value) {
			return
		}
		s := sanitizerFor(t, Policy{AllowedPaths: []string{"/msg"}})
		input, _ := json.Marshal(map[string]any{"msg": "ok", key: value})
		out, r := s.Sanitize(input)
		if !json.Valid(out) {
			t.Fatal("invalid allowlist output")
		}
		if r.ErrorClass != "" {
			return
		}
		var decoded map[string]any
		if err := json.Unmarshal(out, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded) != 1 || decoded["msg"] != "ok" {
			t.Fatal("unlisted field survived")
		}
		if len(value) >= 8 && bytes.Contains(out, []byte(value)) {
			t.Fatal("unlisted value survived")
		}
		for _, p := range r.Paths {
			if p != "/*" {
				t.Fatal("unapproved key entered a report path")
			}
		}
	})
}

func FuzzSanitizeKeys(f *testing.F) {
	f.Add("pass\u200bword")
	f.Add("private person notes")
	f.Add("2001:db8::1.")
	f.Fuzz(func(t *testing.T, key string) {
		if !utf8.ValidString(key) {
			return
		}
		s := sanitizerFor(t, Policy{})
		input, _ := json.Marshal(map[string]any{key: "ordinary unapproved value"})
		out, r := s.Sanitize(input)
		if !json.Valid(out) {
			t.Fatal("key produced invalid JSON")
		}
		if r.ErrorClass == "" && string(out) != "{}" {
			t.Fatal("unlisted key survived")
		}
		for _, p := range r.Paths {
			if p != "/*" {
				t.Fatal("key leaked to report")
			}
		}
	})
}

func TestSanitizeParenthesizedPhoneRuns(t *testing.T) {
	s := sanitizerFor(t, Policy{Mode: Denylist})
	for _, tc := range []struct {
		input, want string
		count       uint32
	}{
		{"call (202) 555-0123 (202) 555-0145", "call [REDACTED] [REDACTED]", 2},
		{"call (202)555-0123 (202)555-0145", "call [REDACTED] [REDACTED]", 2},
		{"call (202) 555-0123 (202)555-0145 (202) 555-0199", "call [REDACTED] [REDACTED] [REDACTED]", 3},
		{"phone(202) 555-0123", "phone[REDACTED]", 1},
	} {
		out, r := s.Sanitize(jsonString(tc.input))
		if !bytes.Equal(out, jsonString(tc.want)) || !r.PII || r.Redactions != tc.count {
			t.Fatal("parenthesized phone run partially escaped")
		}
	}
}
