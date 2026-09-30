package tesseracttest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tesseract "github.com/hollis-labs/go-tesseract-client"
)

// Route names accepted by Fake.Calls, Fake.Fail and Fake.Requests.
const (
	RouteRecall     = "recall"     // POST /v1/memory/recall
	RouteCurrent    = "current"    // GET  /v1/knowledge/current
	RouteRevision   = "revision"   // GET  /v1/memory/revisions/{id}
	RouteDeprecate  = "deprecate"  // POST /v1/memory/deprecate
	RouteNamespaces = "namespaces" // GET  /v1/namespaces/list
	RouteReadiness  = "readiness"  // GET  /v1/health/readiness
)

// Request is one request the fake received.
type Request struct {
	Route    string
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// Failure makes a route answer something other than its normal response.
type Failure struct {
	// Status is the HTTP status; zero means 500.
	Status int
	// Code and Message become Tesseract's {code, message, details} error
	// object. Ignored when Body is set.
	Code    string
	Message string
	// Body, when non-empty, is written verbatim (with Status), so a test can
	// serve a non-JSON page, malformed JSON, or an oversize body.
	Body string
}

// Fake is a running fake Tesseract. Create one with New. Its methods are safe
// for concurrent use.
type Fake struct {
	srv *httptest.Server

	mu       sync.Mutex
	revs     []tesseract.Revision
	calls    map[string]int
	requests []Request
	fails    map[string]Failure
	token    string
	pageSize int
	delay    time.Duration
}

// New starts a fake holding revs and stops it when the test ends. Deprecated
// revisions (Status "deprecated") are omitted from recall but still served by
// GetRevision, as the real service does.
func New(t testing.TB, revs ...tesseract.Revision) *Fake {
	t.Helper()
	f := Start(revs...)
	t.Cleanup(f.Close)
	return f
}

// Start is New for callers without a testing.TB (an Example function, a
// TestMain): the caller must Close the fake.
func Start(revs ...tesseract.Revision) *Fake {
	f := &Fake{revs: slices.Clone(revs), calls: map[string]int{}, fails: map[string]Failure{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/memory/recall", f.route(RouteRecall, true, f.recall))
	mux.HandleFunc("GET /v1/knowledge/current", f.route(RouteCurrent, true, f.current))
	mux.HandleFunc("GET /v1/memory/revisions/{id}", f.route(RouteRevision, true, f.revision))
	mux.HandleFunc("POST /v1/memory/deprecate", f.route(RouteDeprecate, true, f.deprecate))
	mux.HandleFunc("GET /v1/namespaces/list", f.route(RouteNamespaces, true, f.namespaces))
	mux.HandleFunc("GET /v1/health/readiness", f.route(RouteReadiness, false, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	f.srv = httptest.NewServer(mux)
	return f
}

// URL is the fake's base URL.
func (f *Fake) URL() string { return f.srv.URL }

// Close stops the fake early, to test a Tesseract that has gone away.
func (f *Fake) Close() { f.srv.Close() }

// Set replaces the revisions the fake serves.
func (f *Fake) Set(revs ...tesseract.Revision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revs = slices.Clone(revs)
}

// Revisions returns a copy of the revisions the fake currently holds,
// including the effect of any Deprecate call.
func (f *Fake) Revisions() []tesseract.Revision { return f.snapshot() }

// Calls returns how many times a route was hit, whether or not it was made to
// fail.
func (f *Fake) Calls(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[route]
}

// Requests returns every request received so far, oldest first, optionally
// only those for the named routes.
func (f *Fake) Requests(routes ...string) []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Request
	for _, r := range f.requests {
		if len(routes) == 0 || slices.Contains(routes, r.Route) {
			out = append(out, r)
		}
	}
	return out
}

// Fail makes route answer with fail until ClearFailures. It applies before
// the auth check, so it can also simulate an unauthenticated 401.
func (f *Fake) Fail(route string, fail Failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails[route] = fail
}

// ClearFailures removes every Fail.
func (f *Fake) ClearFailures() {
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.fails)
}

// RequireToken makes every route except readiness demand
// "Authorization: Bearer <token>", answering 401 auth_required otherwise, as
// the real service does with a static token configured. An empty token turns
// the requirement off.
func (f *Fake) RequireToken(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = token
}

// PageSize caps how many recall hits or namespaces one page carries,
// whatever limit the caller asked for, so a test can force multiple pages.
// Zero removes the cap.
func (f *Fake) PageSize(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageSize = n
}

// Delay makes every route wait d before answering, to test timeouts and
// context cancellation. Zero removes the delay.
func (f *Fake) Delay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = d
}

func (f *Fake) snapshot() []tesseract.Revision {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.revs)
}

func (f *Fake) route(name string, needsAuth bool, h func(http.ResponseWriter, *http.Request, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls[name]++
		f.requests = append(f.requests, Request{
			Route: name, Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
			Header: r.Header.Clone(), Body: body,
		})
		fail, failing := f.fails[name]
		token, delay := f.token, f.delay
		f.mu.Unlock()

		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if failing {
			writeFailure(w, fail)
			return
		}
		if needsAuth && token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			writeError(w, http.StatusUnauthorized, "auth_required", "missing or invalid bearer token")
			return
		}
		h(w, r, body)
	}
}

func matches(namespace, pattern string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
		return strings.HasPrefix(namespace, prefix+"/")
	}
	return namespace == pattern
}

// recallBody mirrors the server's memoryRecallRequest
// (apps/tesseract/internal/contextapi/memory_handler.go:159) for the fields a
// caller of this library can send, plus the server-only ones it might, so
// that decoding strictly rejects exactly what the real route rejects.
type recallBody struct {
	Namespaces    []string        `json:"namespaces"`
	RevisionScope string          `json:"revision_scope"`
	Ranking       string          `json:"ranking"`
	Query         string          `json:"query"`
	Filters       json.RawMessage `json:"filters"`
	Limit         int             `json:"limit"`
	SearchMode    string          `json:"search_mode"`
	PayloadMode   string          `json:"payload_mode"`
	SimilarityMin *float64        `json:"similarity_min"`
	Cursor        string          `json:"cursor"`
	BudgetBytes   *int            `json:"budget_bytes"`
	BudgetTokens  *int            `json:"budget_tokens"`
	EstimateOnly  bool            `json:"estimate_only"`
}

// recallFilters mirrors memory.RecallFilters as the HTTP door decodes it
// (apps/tesseract/internal/memory/recall.go:85, wrapped at
// memory_handler.go:225): no JSON tags, so keys are Go field names matched
// case-insensitively but not across underscores; unknown keys are refused.
type recallFilters struct {
	DerivedFrom      []string
	Statuses         []string
	Tags             []string
	ConfidenceMin    float64
	Since            *time.Time
	Until            *time.Time
	SimilarityMin    *float64
	Domains          []string
	FacetKinds       []string
	FacetSources     []string
	RelatedTo        []string
	RelatedRelations []string
	PointerHealth    []string
	StateFilters     json.RawMessage `json:"state_filters"`
}

func (f *Fake) recall(w http.ResponseWriter, _ *http.Request, raw []byte) {
	var req recallBody
	if err := strictDecode(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if len(req.Filters) > 0 && string(req.Filters) != "null" {
		var filt recallFilters
		if err := strictDecode(req.Filters, &filt); err != nil {
			writeError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}

	type hit struct {
		rev   tesseract.Revision
		score float64
	}
	var hits []hit
	terms := strings.Fields(strings.ToLower(req.Query))
	for _, rev := range f.snapshot() {
		if rev.Status == "deprecated" {
			continue
		}
		if !slices.ContainsFunc(req.Namespaces, func(ns string) bool { return matches(rev.Namespace, ns) }) {
			continue
		}
		score := 1.0
		if len(terms) > 0 {
			haystack := strings.ToLower(rev.MemoryKey + " " + rev.Payload.Summary + " " + rev.Payload.Body)
			score = 0
			for _, term := range terms {
				if strings.Contains(haystack, term) {
					score++
				}
			}
			if score == 0 {
				continue
			}
		}
		hits = append(hits, hit{rev, score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })

	offset := decodeCursor(req.Cursor)
	limit := req.Limit
	if limit <= 0 {
		limit = 30
	}
	f.mu.Lock()
	if f.pageSize > 0 {
		limit = min(limit, f.pageSize)
	}
	f.mu.Unlock()
	offset = min(offset, len(hits))
	end := min(offset+limit, len(hits))
	page := hits[offset:end]

	results := make([]map[string]any, 0, len(page))
	for _, h := range page {
		entry := map[string]any{"revision": shape(h.rev, req.PayloadMode)}
		if len(terms) > 0 {
			entry["score"] = h.score
		}
		results = append(results, entry)
	}
	var next any
	reason := ""
	if end < len(hits) {
		next = encodeCursor(end)
		reason = "limit"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"facets":  map[string]any{},
		"manifest": map[string]any{
			"results_total":     len(hits),
			"results_returned":  len(page),
			"truncated":         end < len(hits),
			"truncation_reason": reason,
			"next_cursor":       next,
		},
	})
}

// shape projects a revision the way payload_mode does: keys is identity only,
// summary drops the body and data, anything else carries everything.
func shape(rev tesseract.Revision, mode string) tesseract.Revision {
	switch mode {
	case "keys":
		return tesseract.Revision{
			RevisionID: rev.RevisionID, ItemID: rev.ItemID, Domain: rev.Domain,
			Namespace: rev.Namespace, MemoryKey: rev.MemoryKey, CreatedAt: rev.CreatedAt,
		}
	case "summary":
		rev.Payload.Body = ""
		rev.Payload.Data = nil
	}
	return rev
}

func (f *Fake) current(w http.ResponseWriter, r *http.Request, _ []byte) {
	ns, key := r.URL.Query().Get("namespace"), r.URL.Query().Get("key")
	revs := f.snapshot()
	for i := len(revs) - 1; i >= 0; i-- {
		if revs[i].Namespace == ns && revs[i].MemoryKey == key {
			writeJSON(w, http.StatusOK, revs[i])
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found",
		fmt.Sprintf("memory not found: no current revision for %s/%s", ns, key))
}

func (f *Fake) revision(w http.ResponseWriter, r *http.Request, _ []byte) {
	id := r.PathValue("id")
	for _, rev := range f.snapshot() {
		if rev.RevisionID == id {
			writeJSON(w, http.StatusOK, rev)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "memory not found: no revision "+id)
}

func (f *Fake) deprecate(w http.ResponseWriter, _ *http.Request, raw []byte) {
	var req struct {
		RevisionID string `json:"revision_id"`
	}
	if err := strictDecode(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if req.RevisionID == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "revision_id is required")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.revs {
		if f.revs[i].RevisionID == req.RevisionID {
			f.revs[i].Status = "deprecated"
			writeJSON(w, http.StatusOK, map[string]string{"status": "deprecated", "revision_id": req.RevisionID})
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "memory not found: no revision "+req.RevisionID)
}

func (f *Fake) namespaces(w http.ResponseWriter, r *http.Request, _ []byte) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	set := map[string]bool{}
	for _, rev := range f.snapshot() {
		if strings.HasPrefix(rev.Namespace, prefix) {
			set[rev.Namespace] = true
		}
	}
	all := make([]string, 0, len(set))
	for ns := range set {
		all = append(all, ns)
	}
	sort.Strings(all)

	limit := 200
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	f.mu.Lock()
	if f.pageSize > 0 {
		limit = min(limit, f.pageSize)
	}
	f.mu.Unlock()
	offset := min(decodeCursor(q.Get("cursor")), len(all))
	end := min(offset+limit, len(all))
	items := make([]map[string]string, 0, end-offset)
	for _, ns := range all[offset:end] {
		items = append(items, map[string]string{"namespace": ns})
	}
	next := ""
	if end < len(all) {
		next = encodeCursor(end)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": len(all), "truncated": next != "", "next_cursor": next,
	})
}

func encodeCursor(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset)))
}

func decodeCursor(cursor string) int {
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(string(raw), "o:"))
	return max(n, 0)
}

func strictDecode(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "message": message, "details": nil})
}

func writeFailure(w http.ResponseWriter, fail Failure) {
	status := fail.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if fail.Body != "" {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, fail.Body)
		return
	}
	writeError(w, status, fail.Code, fail.Message)
}
