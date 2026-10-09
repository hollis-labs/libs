package toolresult_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
)

var ctx = context.Background()

func TestDefaultsAndULIDShape(t *testing.T) {
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	view, err := cache.Present(ctx, "s", toolresult.Meta{Tool: "t"}, strings.Repeat("x", toolresult.DefaultBudget+1), 0)
	if err != nil || !view.Cached {
		t.Fatalf("a result one byte over the 2048 default budget was not cached: %+v, %v", view, err)
	}
	if view.BudgetBytes != 2048 {
		t.Errorf("BudgetBytes = %d, want 2048", view.BudgetBytes)
	}
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`).MatchString(view.CacheID) {
		t.Errorf("default id %q is not a 26-char Crockford ULID", view.CacheID)
	}
	if d := time.Until(view.Pointer.ExpiresAt); d < 59*time.Minute || d > time.Hour {
		t.Errorf("default TTL: expires in %v, want about 1h", d)
	}
	if v, _ := cache.Present(ctx, "s", toolresult.Meta{}, strings.Repeat("x", 2048), 0); v.Cached {
		t.Error("a result exactly at the budget must pass through")
	}
	other, _ := cache.Present(ctx, "s", toolresult.Meta{Tool: "t"}, strings.Repeat("x", 5000), 0)
	if other.CacheID == view.CacheID {
		t.Error("ids collide")
	}
}

func TestBudgetIsCappedByHardCap(t *testing.T) {
	cache := toolresult.New(memstore.New(), toolresult.Config{HardCapBytes: 1000})
	view, err := cache.Present(ctx, "s", toolresult.Meta{}, strings.Repeat("x", 1200), 1<<20)
	if err != nil || view.BudgetBytes != 1000 || !view.Cached || view.Pointer.Stored {
		t.Fatalf("%+v, %v", view, err)
	}
}

func TestPreviewBudgetExcludesNotice(t *testing.T) {
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	body := strings.Repeat("0123456789\n", 1000)
	view, _ := cache.Present(ctx, "s", toolresult.Meta{}, body, 500)
	preview, _ := toolresult.Preview(body, 500)
	if len(preview) > 500 || !strings.Contains(view.Content, preview) || len(view.Content) <= 500 {
		t.Fatalf("preview %d bytes, content %d bytes", len(preview), len(view.Content))
	}
	if !strings.HasPrefix(view.Content, "[PARTIAL PREVIEW — not a complete read.") {
		t.Errorf("missing partial-preview header: %q", view.Content[:80])
	}
}

func TestPresentDoesNotModifyStoredOriginal(t *testing.T) {
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	body, _ := json.MarshalIndent(map[string]any{"id": "x", "text": strings.Repeat("ab ", 3000)}, "", "  ")
	view, _ := cache.Present(ctx, "s", toolresult.Meta{}, string(body), 1000)
	page, err := cache.Read(ctx, "s", view.CacheID, "", 0, 1<<20, 1<<20)
	if err != nil || page.Content != string(body) {
		t.Fatalf("stored original differs (err %v)", err)
	}
}

func TestCustomSchemeNamesAndFooter(t *testing.T) {
	var got toolresult.FooterData
	cache := toolresult.New(memstore.New(), toolresult.Config{
		Scheme: "wiki_result", FetchName: "loom_fetch_result", SearchName: "loom_search_result",
		NewID: func() string { return "abc" },
		Now:   func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) },
	})
	view, _ := cache.Present(ctx, "s", toolresult.Meta{Tool: "wiki_search"}, strings.Repeat("x", 3000), 0)
	for _, want := range []string{"wiki_result://abc", `loom_fetch_result({"id":"abc"})`, `loom_search_result({"id":"abc"`, "expires_at=2026-03-04T06:06:07Z"} {
		if !strings.Contains(view.Content, want) {
			t.Errorf("footer lost %q: %s", want, view.Footer)
		}
	}
	if spec := cache.FetchSpec(); spec.Name != "loom_fetch_result" || !strings.Contains(spec.Description, "wiki_result:// pointer") {
		t.Errorf("FetchSpec: %+v", spec)
	}
	if spec := cache.SearchSpec(); spec.Name != "loom_search_result" || !strings.Contains(spec.Description, "usable by loom_fetch_result") {
		t.Errorf("SearchSpec: %+v", spec)
	}

	custom := toolresult.New(memstore.New(), toolresult.Config{
		NewID: func() string { return "zzz" },
		Footer: func(d toolresult.FooterData) string {
			got = d
			return "SEE " + d.ID
		},
	})
	view, _ = custom.Present(ctx, "s", toolresult.Meta{Tool: "tt"}, strings.Repeat("x", 3000), 0)
	if view.Footer != "SEE zzz" || !strings.HasSuffix(view.Content, "\n\nSEE zzz") {
		t.Errorf("custom footer: %q", view.Content[len(view.Content)-20:])
	}
	if got.ID != "zzz" || got.Tool != "tt" || got.ByteSize != 3000 || !got.Stored || got.HardCapBytes != toolresult.DefaultHardCapBytes || got.Scheme != "tool_result" {
		t.Errorf("FooterData: %+v", got)
	}
	silent := toolresult.New(memstore.New(), toolresult.Config{Footer: func(toolresult.FooterData) string { return "" }})
	view, _ = silent.Present(ctx, "s", toolresult.Meta{}, strings.Repeat("x", 3000), 0)
	if view.Footer != "" || strings.HasSuffix(view.Content, "\n\n") {
		t.Error("an empty footer must add nothing")
	}
}

func TestTTLIsConfigurable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache := toolresult.New(memstore.New(), toolresult.Config{TTL: 24 * 365 * time.Hour, Now: func() time.Time { return now }})
	ptr, err := cache.Put(ctx, "s", toolresult.Meta{}, "x")
	if err != nil || !ptr.ExpiresAt.Equal(now.Add(24*365*time.Hour)) {
		t.Fatalf("%+v, %v", ptr, err)
	}
}

func TestSpecsAreFreshCopiesWithNaniteSchema(t *testing.T) {
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	f := cache.FetchSpec()
	if f.Name != "fetch_tool_result" || f.InputSchema["additionalProperties"] != false {
		t.Fatalf("%+v", f)
	}
	props := f.InputSchema["properties"].(map[string]any)
	for _, k := range []string{"id", "json_pointer", "offset", "length"} {
		if _, ok := props[k]; !ok {
			t.Errorf("fetch schema lacks %q", k)
		}
	}
	if req := f.InputSchema["required"].([]any); len(req) != 1 || req[0] != "id" {
		t.Errorf("fetch required = %v", req)
	}
	s := cache.SearchSpec()
	if s.Name != "search_tool_result" {
		t.Fatalf("%+v", s)
	}
	if req := s.InputSchema["required"].([]any); len(req) != 2 {
		t.Errorf("search required = %v", req)
	}
	for _, k := range []string{"id", "pattern", "json_pointer", "offset", "max_matches"} {
		if _, ok := s.InputSchema["properties"].(map[string]any)[k]; !ok {
			t.Errorf("search schema lacks %q", k)
		}
	}
	f.InputSchema["properties"].(map[string]any)["id"] = "mutated"
	if cache.FetchSpec().InputSchema["properties"].(map[string]any)["id"] == "mutated" {
		t.Error("FetchSpec returned shared state")
	}
	if _, err := json.Marshal(f.InputSchema); err != nil {
		t.Errorf("schema is not JSON-serializable: %v", err)
	}
}

func TestHandlersAcceptDecodedJSONNumbers(t *testing.T) {
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	ptr, _ := cache.Put(ctx, "s", toolresult.Meta{}, "0123456789")
	var in map[string]any
	if err := json.Unmarshal([]byte(`{"id":"`+ptr.ID+`","offset":2,"length":3}`), &in); err != nil {
		t.Fatal(err)
	}
	text, isErr := cache.HandleFetch(ctx, "s", in, 100)
	if isErr || !strings.HasSuffix(text, "\n\n234") || !strings.Contains(text, "bytes 2..5 of 10") {
		t.Fatalf("%q", text)
	}
	dec := json.NewDecoder(strings.NewReader(`{"id":"` + ptr.ID + `","offset":1}`))
	dec.UseNumber()
	var in2 map[string]any
	_ = dec.Decode(&in2)
	if text, isErr = cache.HandleFetch(ctx, "s", in2, 100); isErr || !strings.Contains(text, "bytes 1..10") {
		t.Fatalf("json.Number offset: %q", text)
	}
	if text, isErr = cache.HandleFetch(ctx, "s", map[string]any{"id": ptr.ID, "offset": int64(4)}, 100); isErr || !strings.Contains(text, "bytes 4..10") {
		t.Fatalf("int64 offset: %q", text)
	}
}

// TestTruncatedTaskRecordIsRecoverable carries over Nanite's CW-20260815-0020
// regression: with production defaults a realistic task record is previewed,
// and every field is recoverable by fetch, pointer and search.
func TestTruncatedTaskRecordIsRecoverable(t *testing.T) {
	var body string
	for _, c := range goldenCases() {
		if c.name == "torque_task" {
			body = c.body
		}
	}
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	if len(body) <= toolresult.DefaultBudget {
		t.Fatalf("fixture (%d bytes) no longer exceeds the default budget", len(body))
	}
	view, err := cache.Present(ctx, "sess-torque", toolresult.Meta{Tool: "torque_task_get"}, body, 0)
	if err != nil || !view.Cached || !strings.Contains(view.Content, "tool_result://"+view.CacheID) {
		t.Fatalf("%+v, %v", view, err)
	}
	if !strings.Contains(view.Content, "[OMITTED bytes") {
		t.Fatal("the long Description must be elided in the preview")
	}
	page, err := cache.Read(ctx, "sess-torque", view.CacheID, "", 0, 0, 1<<20)
	if err != nil || page.TotalBytes != len(body) || !strings.Contains(page.Content, "CW-20260814-0013") {
		t.Fatalf("Read: %v", err)
	}
	res, err := cache.Search(ctx, "sess-torque", view.CacheID, "", "DependsOn", 0, 5, 4000)
	if err != nil || len(res.Matches) == 0 || !strings.Contains(res.Matches[0].Context, "CW-20260814-0013") {
		t.Fatalf("Search: %+v, %v", res, err)
	}
	dep, err := cache.Read(ctx, "sess-torque", view.CacheID, "/DependsOn/String", 0, 0, 0)
	if err != nil || dep.Content != `["CW-20260814-0013","CW-20260814-0014"]` {
		t.Fatalf("pointer read: %+v, %v", dep, err)
	}
}

func TestLineBoundaryPreviewEndsOnCompleteLines(t *testing.T) {
	var b strings.Builder
	for i := range 10 {
		b.WriteString(strings.Repeat(string(rune('A'+i)), 29))
		b.WriteByte('\n')
	}
	cache := toolresult.New(memstore.New(), toolresult.Config{})
	view, _ := cache.Present(ctx, "s", toolresult.Meta{}, b.String(), 100)
	preview := view.Content[len("[PARTIAL PREVIEW — not a complete read. Omitted content must be retrieved before making claims about it.]\n"):strings.Index(view.Content, "\n\n[TRUNCATED")]
	for _, line := range strings.Split(preview, "\n") {
		if line != "" && len(line) != 29 && !strings.HasPrefix(line, "[OMITTED") {
			t.Errorf("partial line %q in %q", line, preview)
		}
	}
}
