package transportparity

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPJSONInvokesTheHandlerDirectly(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotCT, gotBody string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotCT = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	status, body := HTTPJSON(t, h, http.MethodPost, "/v1/things?x=1", map[string]any{"a": 1})
	if status != 201 || string(body) != `{"ok":true}` {
		t.Errorf("status %d body %s", status, body)
	}
	if gotMethod != "POST" || gotPath != "/v1/things" || gotQuery != "x=1" || gotCT != "application/json" || gotBody != `{"a":1}` {
		t.Errorf("handler saw %s %s ?%s ct=%q body=%q", gotMethod, gotPath, gotQuery, gotCT, gotBody)
	}
}

func TestHTTPJSONWithoutABodySendsNoneAndNoContentType(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) != 0 || r.Header.Get("Content-Type") != "" {
			t.Errorf("unexpected body %q or content type %q", b, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	status, body := HTTPJSON(t, h, http.MethodGet, "/x", nil)
	if status != 204 || body != nil {
		t.Errorf("status %d body %q", status, body)
	}
}

func TestHTTPJSONReportsANonJSONBody(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "task not found", http.StatusNotFound)
	})
	rec := &recorder{}
	status, body := HTTPJSON(rec, h, http.MethodGet, "/x", nil)
	if status != 404 || body != nil {
		t.Errorf("status %d body %q", status, body)
	}
	if !strings.Contains(rec.text(), "not JSON") || !strings.Contains(rec.text(), "task not found") {
		t.Errorf("got %q, want a complaint naming the body", rec.text())
	}
}

func TestHTTPJSONReportsAnUnmarshalableRequestBody(t *testing.T) {
	rec := &recorder{}
	HTTPJSON(rec, http.NotFoundHandler(), http.MethodPost, "/x", make(chan int))
	if !rec.fatal {
		t.Error("an unmarshalable request body did not fail the test")
	}
}

// The recorded body is valid JSON a caller can hand straight to Outcome.
func TestHTTPJSONBodyFeedsAnOutcome(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"id": 7}`)) })
	_, body := HTTPJSON(t, h, http.MethodGet, "/x", nil)
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil || v["id"] != float64(7) {
		t.Errorf("body %s -> %v, %v", body, v, err)
	}
}
