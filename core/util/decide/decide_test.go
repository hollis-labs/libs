package decide

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixtureKey is made up; it is built at run time so scanners do not read it as a credential.
var fixtureKey = strings.Repeat("k", 12) + "-fixture-" + strings.Repeat("9", 12)

func actRequest() Request {
	return Request{
		State:   map[string]any{"text": "fix the build"},
		Purpose: "classify",
		Model:   "jev-1.13.0",
		Questions: []Question{{Key: "act", Type: Choice, Instructions: "What does it ask?",
			Options: []Option{{Key: "task", Description: "do something"}, {Key: "question", Description: "ask something"}}}},
	}
}

func ok(w http.ResponseWriter, answers string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"response":{"provider":"jev","model":"jev-1.13.0","decision":{"answers":` + answers +
		`,"latency_ms":88,"request_id":"r-1"},"usage":{"input_tokens":40,"output_tokens":1,"estimated_cost_usd":0.000002}}}`))
}

func client(srv *httptest.Server) *Client {
	c := NewClient(srv.URL, fixtureKey, "test")
	c.Backoff = time.Millisecond
	return c
}

func TestClientSendsTheWireShapeAndReadsAnswers(t *testing.T) {
	var got map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		ok(w, `{"act":{"type":"choice","choice":"task","probabilities":{"task":0.9,"question":0.1}}}`)
	}))
	defer srv.Close()
	resp, err := client(srv).Decide(context.Background(), actRequest())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/ai/decide" || auth != "Bearer "+fixtureKey {
		t.Fatalf("path %q auth %q", path, auth)
	}
	req := got["request"].(map[string]any)
	dec := req["decision"].(map[string]any)
	if req["caller_id"] != "test" || req["model_hint"] != "jev-1.13.0" || dec["purpose"] != "classify" || dec["on_error"] != "fail_open" {
		t.Fatalf("request %v", got)
	}
	a := resp.Answers["act"]
	if a.Choice != "task" || a.Confidence < 0.79 || a.Confidence > 0.81 || resp.Latency != 88*time.Millisecond || resp.Usage.InputTokens != 40 {
		t.Fatalf("response %+v", resp)
	}
}

func TestClientRefusesBeforeCalling(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := client(srv)
	for name, mut := range map[string]func(*Request){
		"no purpose":   func(r *Request) { r.Purpose = "" },
		"no questions": func(r *Request) { r.Questions = nil },
		"one option":   func(r *Request) { r.Questions[0].Options = r.Questions[0].Options[:1] },
		"bad type":     func(r *Request) { r.Questions[0].Type = "essay" },
		"dup key": func(r *Request) {
			r.Questions = append(r.Questions, r.Questions[0])
		},
	} {
		r := actRequest()
		mut(&r)
		if _, err := c.Decide(context.Background(), r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d calls for invalid requests", calls.Load())
	}
}

func TestClientRejectsAnswersThatDoNotFit(t *testing.T) {
	for name, answers := range map[string]string{
		"missing":       `{}`,
		"wrong type":    `{"act":{"type":"noul","noul":0.5}}`,
		"no choice":     `{"act":{"type":"choice","probabilities":{"task":1}}}`,
		"unoffered":     `{"act":{"type":"choice","choice":"poem","probabilities":{"poem":1}}}`,
		"probability>1": `{"act":{"type":"choice","choice":"task","probabilities":{"task":1.5}}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ok(w, answers) }))
		if _, err := client(srv).Decide(context.Background(), actRequest()); err == nil {
			t.Errorf("%s: accepted", name)
		}
		srv.Close()
	}
}

func TestClientRetriesOnlyWhatCanSucceed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"decision_failed","message":"slow down","details":{"kind":"rate_limited"}}}`))
			return
		}
		ok(w, `{"act":{"type":"choice","choice":"task","probabilities":{"task":1}}}`)
	}))
	defer srv.Close()
	if _, err := client(srv).Decide(context.Background(), actRequest()); err != nil || calls.Load() != 3 {
		t.Fatalf("err %v after %d calls", err, calls.Load())
	}

	calls.Store(0)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"decision_failed","message":"bad key ` + fixtureKey + `","details":{"kind":"auth"}}}`))
	}))
	defer auth.Close()
	_, err := client(auth).Decide(context.Background(), actRequest())
	var e *Error
	if !errors.As(err, &e) || e.Kind != "auth" || calls.Load() != 1 {
		t.Fatalf("auth failure: %v after %d calls", err, calls.Load())
	}
	if strings.Contains(err.Error(), fixtureKey) {
		t.Fatal("the token leaked into an error")
	}
}

func TestNewClientWithoutURLIsNil(t *testing.T) {
	if NewClient("  ", "t", "c") != nil {
		t.Fatal("client without a URL")
	}
	if c := NewClient("unix:///tmp/x.sock", "", ""); c == nil || c.BaseURL != "http://unix" {
		t.Fatalf("unix client %+v", c)
	}
}

func TestScrub(t *testing.T) {
	// Credential-shaped values are built at run time so scanners do not flag the fixture.
	in := "use " + "sk" + "-" + strings.Repeat("a", 24) + " and " + "gh" + "p_" + strings.Repeat("b", 36) + " please"
	out := Scrub(in)
	if strings.Contains(out, "aaaa") || strings.Contains(out, "bbbb") || !strings.Contains(out, "please") {
		t.Fatalf("scrub %q", out)
	}
}

func TestConfidence(t *testing.T) {
	if c := ChoiceConfidence(map[string]float64{"a": 0.5, "b": 0.5}); c != 0 {
		t.Errorf("even split %v", c)
	}
	if c := ChoiceConfidence(map[string]float64{"a": 1, "b": 0}); c != 1 {
		t.Errorf("certain %v", c)
	}
	if c := NoulConfidence(0.5); c != 0 {
		t.Errorf("noul %v", c)
	}
	if lv := ScoreLevels(map[string]float64{"1": 0.2, "0": 0.8}); lv[0] != 0.8 || lv[1] != 0.2 {
		t.Errorf("levels %v", lv)
	}
}
