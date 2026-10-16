package host

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// FrameDocument mirrors the shared browser delivery value; it does not decide
// isolation or trust. Admit must check exact reviewed HTML and response policy.
type FrameDocument struct {
	FrameID           string `json:"frameId"`
	HTML              string `json:"html"`
	CSP               string `json:"csp"`
	PermissionsPolicy string `json:"permissionsPolicy"`
}
type FrameDocumentsConfig struct {
	Admit    func(*http.Request, FrameDocument) bool
	Capacity int
	MaxBytes int64
	Lifetime time.Duration
}
type admittedFrame struct {
	document FrameDocument
	expires  time.Time
}

// FrameDocuments is an optional bounded app-mounted document delivery store.
// Mount behind app authentication/Origin policy. No endpoint is installed by New.
type FrameDocuments struct {
	mu     sync.Mutex
	config FrameDocumentsConfig
	docs   map[string]admittedFrame
}

func NewFrameDocuments(config FrameDocumentsConfig) (*FrameDocuments, error) {
	if config.Admit == nil {
		return nil, fmt.Errorf("host: frame admission policy required")
	}
	if config.Capacity == 0 {
		config.Capacity = 128
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = 2 << 20
	}
	if config.Lifetime == 0 {
		config.Lifetime = 5 * time.Minute
	}
	if config.Capacity < 1 || config.Capacity > 1024 || config.MaxBytes < 1 || config.MaxBytes > 8<<20 || config.Lifetime <= 0 || config.Lifetime > time.Hour {
		return nil, fmt.Errorf("host: invalid frame delivery bounds")
	}
	return &FrameDocuments{config: config, docs: map[string]admittedFrame{}}, nil
}

var frameDocumentID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (d *FrameDocuments) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimPrefix(r.URL.Path, "/")
	if r.Method == http.MethodPost && id == "" {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, d.config.MaxBytes))
		decoder.DisallowUnknownFields()
		var document FrameDocument
		if err := decoder.Decode(&document); err != nil {
			http.Error(w, "invalid frame document", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF || !frameDocumentID.MatchString(document.FrameID) || document.HTML == "" || document.CSP == "" || document.PermissionsPolicy == "" || strings.ContainsAny(document.CSP, "\r\n") || strings.ContainsAny(document.PermissionsPolicy, "\r\n") || !d.config.Admit(r, document) {
			http.Error(w, "frame policy unavailable", http.StatusForbidden)
			return
		}
		d.mu.Lock()
		now := time.Now()
		d.expire(now)
		if _, exists := d.docs[document.FrameID]; exists {
			d.mu.Unlock()
			http.Error(w, "frame already provisioned", http.StatusConflict)
			return
		}
		if len(d.docs) >= d.config.Capacity {
			d.mu.Unlock()
			http.Error(w, "frame capacity unavailable", http.StatusServiceUnavailable)
			return
		}
		d.docs[document.FrameID] = admittedFrame{document: document, expires: now.Add(d.config.Lifetime)}
		d.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		return
	}
	if !frameDocumentID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	d.mu.Lock()
	d.expire(time.Now())
	frame, exists := d.docs[id]
	if r.Method == http.MethodDelete && exists {
		delete(d.docs, id)
	}
	d.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Immutable snapshot before network writes: cleanup never waits for a slow frame.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", frame.document.CSP)
	w.Header().Set("Permissions-Policy", frame.document.PermissionsPolicy)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, frame.document.HTML)
}
func (d *FrameDocuments) expire(now time.Time) {
	for id, frame := range d.docs {
		if !now.Before(frame.expires) {
			delete(d.docs, id)
		}
	}
}

// Clear releases retained document bytes. The consumer separately disposes frames
// and their controller; deleting a document cannot recall already executed code.
func (d *FrameDocuments) Clear() { d.mu.Lock(); d.docs = map[string]admittedFrame{}; d.mu.Unlock() }
