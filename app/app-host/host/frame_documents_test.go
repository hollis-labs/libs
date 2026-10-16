package host

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func provisionTestFrame(t *testing.T, store *FrameDocuments, document FrameDocument) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(document)
	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("POST", "/", bytes.NewReader(body)))
	return w
}
func TestFrameDocumentDeliveryRequiresAdmissionAndExactPolicies(t *testing.T) {
	if _, err := NewFrameDocuments(FrameDocumentsConfig{}); err == nil {
		t.Fatal("missing app policy accepted")
	}
	document := FrameDocument{FrameID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HTML: "<!doctype html><p>reviewed</p>", CSP: "default-src 'none'", PermissionsPolicy: "camera=()"}
	store, err := NewFrameDocuments(FrameDocumentsConfig{Admit: func(_ *http.Request, value FrameDocument) bool { return value == document }, Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	bad := document
	bad.CSP = "default-src *"
	if w := provisionTestFrame(t, store, bad); w.Code != 403 {
		t.Fatalf("unreviewed policy: %d", w.Code)
	}
	if w := provisionTestFrame(t, store, document); w.Code != 201 {
		t.Fatalf("provision: %d %s", w.Code, w.Body.String())
	}
	if w := provisionTestFrame(t, store, document); w.Code != 409 {
		t.Fatal("document identity overwritten")
	}
	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("GET", "/"+document.FrameID, nil))
	if w.Code != 200 || w.Body.String() != document.HTML || w.Header().Get("Content-Security-Policy") != document.CSP || w.Header().Get("Permissions-Policy") != document.PermissionsPolicy || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response differs from exact admitted document")
	}
	w = httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("DELETE", "/"+document.FrameID, nil))
	if w.Code != 204 {
		t.Fatal("release failed")
	}
	w = httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("GET", "/"+document.FrameID, nil))
	if w.Code != 404 {
		t.Fatal("released bytes served")
	}
}
func TestFrameDocumentsCapacityExpirationAndClear(t *testing.T) {
	store, _ := NewFrameDocuments(FrameDocumentsConfig{Admit: func(*http.Request, FrameDocument) bool { return true }, Capacity: 1})
	first := FrameDocument{FrameID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", HTML: "html", CSP: "fixed", PermissionsPolicy: "fixed"}
	second := first
	second.FrameID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if provisionTestFrame(t, store, first).Code != 201 || provisionTestFrame(t, store, second).Code != 503 {
		t.Fatal("unbounded store")
	}
	frame := store.docs[first.FrameID]
	frame.expires = time.Now().Add(-time.Second)
	store.docs[first.FrameID] = frame
	if provisionTestFrame(t, store, second).Code != 201 {
		t.Fatal("expired document retained")
	}
	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("GET", "/"+first.FrameID, nil))
	if w.Code != 404 {
		t.Fatal("expired bytes served")
	}
	store.Clear()
	if len(store.docs) != 0 {
		t.Fatal("clear retained bytes")
	}
}

type stalledFrameWriter struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *stalledFrameWriter) Header() http.Header { return w.header }
func (w *stalledFrameWriter) WriteHeader(int)     { w.once.Do(func() { close(w.started) }); <-w.release }
func (w *stalledFrameWriter) Write(value []byte) (int, error) {
	w.WriteHeader(200)
	return len(value), nil
}
func TestFrameCleanupDoesNotWaitForNetworkWrites(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			store, _ := NewFrameDocuments(FrameDocumentsConfig{Admit: func(*http.Request, FrameDocument) bool { return true }})
			document := FrameDocument{FrameID: strings.Repeat("a", 64), HTML: "html", CSP: "fixed", PermissionsPolicy: "fixed"}
			if provisionTestFrame(t, store, document).Code != 201 {
				t.Fatal("setup failed")
			}
			path := "/" + document.FrameID
			body, _ := json.Marshal(document)
			if method == "POST" {
				path = "/"
			}
			writer := &stalledFrameWriter{header: http.Header{}, started: make(chan struct{}), release: make(chan struct{})}
			served := make(chan struct{})
			defer func() { close(writer.release); <-served }()
			go func() {
				store.ServeHTTP(writer, httptest.NewRequest(method, path, bytes.NewReader(body)))
				close(served)
			}()
			<-writer.started
			cleared := make(chan struct{})
			go func() { store.Clear(); close(cleared) }()
			select {
			case <-cleared:
			case <-time.After(time.Second):
				t.Fatal("network write blocks cleanup")
			}
		})
	}
}
