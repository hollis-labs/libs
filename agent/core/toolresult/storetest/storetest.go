package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	toolresult "github.com/hollis-labs/go-toolresult"
)

// t0 is the fake clock's start. It is whole-second so stores that truncate
// times to seconds agree with it.
var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	store toolresult.Store
	clock *clock
	cache *toolresult.Cache
}

func newFixture(t *testing.T, newStore func(t *testing.T) toolresult.Store, cfg toolresult.Config) *fixture {
	t.Helper()
	f := &fixture{store: newStore(t), clock: &clock{t: t0}}
	var n int
	var mu sync.Mutex
	cfg.Now = f.clock.now
	cfg.NewID = func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("ID%024d", n)
	}
	f.cache = toolresult.New(f.store, cfg)
	return f
}

// Run executes the conformance suite. newStore must return a fresh, empty
// store for every call (register cleanup with t.Cleanup).
func Run(t *testing.T, newStore func(t *testing.T) toolresult.Store) {
	t.Helper()
	t.Run("Store", func(t *testing.T) { runStore(t, newStore) })
	t.Run("Cache", func(t *testing.T) { runCache(t, newStore) })
}

func runStore(t *testing.T, newStore func(t *testing.T) toolresult.Store) {
	ctx := context.Background()
	entry := func(id, scope string, expires time.Time) toolresult.Entry {
		return toolresult.Entry{ID: id, Scope: scope, Tool: "tool", CallID: "call", CreatedAt: t0, ExpiresAt: expires, ByteSize: 9, Body: "héllo 🙂\n", BodyStored: true}
	}

	t.Run("RoundTrip", func(t *testing.T) {
		s := newStore(t)
		want := entry("a", "s1", t0.Add(time.Hour))
		if err := s.Put(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, "s1", "a")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != want.ID || got.Scope != want.Scope || got.Tool != want.Tool || got.CallID != want.CallID ||
			got.ByteSize != want.ByteSize || got.Body != want.Body || !got.BodyStored ||
			!got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
			t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("MetadataOnlyEntry", func(t *testing.T) {
		s := newStore(t)
		e := entry("m", "s1", t0.Add(time.Hour))
		e.Body, e.BodyStored, e.ByteSize = "", false, 5000
		if err := s.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, "s1", "m")
		if err != nil {
			t.Fatal(err)
		}
		if got.BodyStored || got.Body != "" || got.ByteSize != 5000 {
			t.Fatalf("metadata-only entry came back as %+v", got)
		}
	})

	t.Run("EmptyBodyIsStillStored", func(t *testing.T) {
		s := newStore(t)
		e := entry("e", "s1", t0.Add(time.Hour))
		e.Body, e.ByteSize = "", 0
		if err := s.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, "s1", "e")
		if err != nil || !got.BodyStored {
			t.Fatalf("empty stored body lost its BodyStored flag: %+v, %v", got, err)
		}
	})

	t.Run("CrossScopeAndAbsentAreIndistinguishable", func(t *testing.T) {
		s := newStore(t)
		if err := s.Put(ctx, entry("a", "s1", t0.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
		_, other := s.Get(ctx, "s2", "a")
		_, absent := s.Get(ctx, "s1", "nope")
		if !errors.Is(other, toolresult.ErrNotFound) || !errors.Is(absent, toolresult.ErrNotFound) {
			t.Fatalf("want ErrNotFound for both, got %v and %v", other, absent)
		}
		if other.Error() != absent.Error() {
			t.Fatalf("errors differ: %q vs %q", other, absent)
		}
	})

	t.Run("DuplicateIDFails", func(t *testing.T) {
		s := newStore(t)
		if err := s.Put(ctx, entry("a", "s1", t0.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, entry("a", "s2", t0.Add(time.Hour))); err == nil {
			t.Fatal("a second Put with the same id succeeded; ids are unique across scopes")
		}
		if _, err := s.Get(ctx, "s2", "a"); !errors.Is(err, toolresult.ErrNotFound) {
			t.Fatalf("failed duplicate leaked into scope s2: %v", err)
		}
	})

	t.Run("DeleteExpired", func(t *testing.T) {
		s := newStore(t)
		for _, e := range []toolresult.Entry{
			entry("old1", "s1", t0.Add(-2*time.Hour)),
			entry("old2", "s2", t0.Add(-time.Minute)),
			entry("live", "s1", t0.Add(time.Hour)),
		} {
			if err := s.Put(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.DeleteExpired(ctx, t0)
		if err != nil || n != 2 {
			t.Fatalf("DeleteExpired = %d, %v; want 2, nil", n, err)
		}
		if _, err := s.Get(ctx, "s1", "old1"); !errors.Is(err, toolresult.ErrNotFound) {
			t.Fatalf("expired entry survived: %v", err)
		}
		if _, err := s.Get(ctx, "s1", "live"); err != nil {
			t.Fatalf("live entry deleted: %v", err)
		}
		if n, err := s.DeleteExpired(ctx, t0); err != nil || n != 0 {
			t.Fatalf("second DeleteExpired = %d, %v; want 0, nil", n, err)
		}
	})

	t.Run("ConcurrentUse", func(t *testing.T) {
		s := newStore(t)
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id, scope := fmt.Sprintf("c%02d", i), fmt.Sprintf("s%d", i%3)
				if err := s.Put(ctx, entry(id, scope, t0.Add(time.Hour))); err != nil {
					t.Errorf("Put %s: %v", id, err)
					return
				}
				if _, err := s.Get(ctx, scope, id); err != nil {
					t.Errorf("Get %s: %v", id, err)
				}
				if _, err := s.DeleteExpired(ctx, t0); err != nil {
					t.Errorf("DeleteExpired: %v", err)
				}
			}()
		}
		wg.Wait()
	})
}

func runCache(t *testing.T, newStore func(t *testing.T) toolresult.Store) {
	ctx := context.Background()
	meta := toolresult.Meta{Tool: "test_tool", CallID: "call-1"}
	small := toolresult.Config{HardCapBytes: 1000, DefaultBudget: 100}

	t.Run("SmallResultPassesThroughUncached", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		view, err := f.cache.Present(ctx, "s", meta, "small result", 0)
		if err != nil || view.Cached || view.Content != "small result" || view.Format != "complete" || view.CacheID != "" {
			t.Fatalf("small result: %+v, %v", view, err)
		}
		if n, _ := f.cache.Purge(ctx); n != 0 {
			t.Fatal("something was stored")
		}
	})

	t.Run("LargeResultStoreAndFetch", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		body := strings.Repeat("x", 200)
		view, err := f.cache.Present(ctx, "s", meta, body, 0)
		if err != nil || !view.Cached {
			t.Fatalf("Present: %+v, %v", view, err)
		}
		if !strings.Contains(view.Content, "[TRUNCATED") || !strings.Contains(view.Content, "tool_result://"+view.CacheID) {
			t.Fatalf("no pointer footer: %s", view.Content)
		}
		if view.Footer == "" || !strings.HasSuffix(view.Content, view.Footer) {
			t.Fatal("Footer is not the tail of Content")
		}
		if view.Pointer.ID != view.CacheID || !view.Pointer.Stored || view.Pointer.ByteSize != 200 || !view.Pointer.ExpiresAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("pointer: %+v", view.Pointer)
		}
		page, err := f.cache.Read(ctx, "s", view.CacheID, "", 0, 0, 1000)
		if err != nil || page.Content != body || page.HasMore || page.TotalBytes != 200 {
			t.Fatalf("Read: %+v, %v", page, err)
		}
		page, err = f.cache.Read(ctx, "s", view.CacheID, "", 10, 20, 1000)
		if err != nil || page.Content != body[10:30] || page.Offset != 10 || page.End != 30 || !page.HasMore {
			t.Fatalf("Read slice: %+v, %v", page, err)
		}
	})

	t.Run("PutStoresWithoutPreview", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		ptr, err := f.cache.Put(ctx, "s", meta, "tiny")
		if err != nil || !ptr.Stored || ptr.ByteSize != 4 {
			t.Fatalf("Put: %+v, %v", ptr, err)
		}
		page, err := f.cache.Read(ctx, "s", ptr.ID, "", 0, 0, 0)
		if err != nil || page.Content != "tiny" {
			t.Fatalf("Read: %+v, %v", page, err)
		}
	})

	t.Run("BodyOverHardCapIsMetadataOnly", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		view, err := f.cache.Present(ctx, "s", meta, strings.Repeat("x", 1500), 0)
		if err != nil || !view.Cached || view.Pointer.Stored {
			t.Fatalf("Present: %+v, %v", view, err)
		}
		if !strings.Contains(view.Content, "exceeded the 1000-byte storage cap") || !strings.Contains(view.Content, "full content is unavailable") {
			t.Fatalf("footer does not report the storage cap: %s", view.Content)
		}
		_, err = f.cache.Read(ctx, "s", view.CacheID, "", 0, 0, 0)
		if !errors.Is(err, toolresult.ErrBodyNotStored) || !strings.Contains(err.Error(), "hard cap") {
			t.Fatalf("Read = %v; want ErrBodyNotStored mentioning the hard cap", err)
		}
		if _, err = f.cache.Search(ctx, "s", view.CacheID, "", "x", 0, 1, 0); !errors.Is(err, toolresult.ErrBodyNotStored) {
			t.Fatalf("Search = %v; want ErrBodyNotStored", err)
		}
	})

	t.Run("CrossScopeReadIsNotFound", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		view, _ := f.cache.Present(ctx, "owner", meta, strings.Repeat("needle\n", 60), 0)
		_, errRead := f.cache.Read(ctx, "other", view.CacheID, "", 0, 0, 0)
		_, errSearch := f.cache.Search(ctx, "other", view.CacheID, "", "needle", 0, 5, 0)
		_, errAbsent := f.cache.Read(ctx, "owner", "absent", "", 0, 0, 0)
		if !errors.Is(errRead, toolresult.ErrNotFound) || !errors.Is(errSearch, toolresult.ErrNotFound) || !errors.Is(errAbsent, toolresult.ErrNotFound) {
			t.Fatalf("want ErrNotFound: %v / %v / %v", errRead, errSearch, errAbsent)
		}
		if !strings.Contains(errRead.Error(), "not found or expired") {
			t.Fatalf("error lost the Nanite wording: %v", errRead)
		}
		if _, err := f.cache.Read(ctx, "owner", view.CacheID, "", 0, 0, 0); err != nil {
			t.Fatalf("owner read failed: %v", err)
		}
	})

	t.Run("ExpiredIsErrExpiredAndPurgeable", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		view, _ := f.cache.Present(ctx, "s", meta, strings.Repeat("x", 200), 0)
		f.clock.advance(time.Hour) // exactly at expiry: still readable
		if _, err := f.cache.Read(ctx, "s", view.CacheID, "", 0, 0, 0); err != nil {
			t.Fatalf("read at the expiry instant: %v", err)
		}
		f.clock.advance(time.Second)
		_, err := f.cache.Read(ctx, "s", view.CacheID, "", 0, 0, 0)
		if !errors.Is(err, toolresult.ErrExpired) || !strings.Contains(err.Error(), "has expired") {
			t.Fatalf("Read = %v; want ErrExpired", err)
		}
		if _, err := f.cache.Search(ctx, "s", view.CacheID, "", "x", 0, 1, 0); !errors.Is(err, toolresult.ErrExpired) {
			t.Fatalf("Search = %v; want ErrExpired", err)
		}
		if n, err := f.cache.Purge(ctx); err != nil || n != 1 {
			t.Fatalf("Purge = %d, %v; want 1, nil", n, err)
		}
		if _, err := f.cache.Read(ctx, "s", view.CacheID, "", 0, 0, 0); !errors.Is(err, toolresult.ErrNotFound) {
			t.Fatalf("purged entry: %v", err)
		}
	})

	t.Run("ErrorsAndExemptToolsPassThroughUncached", func(t *testing.T) {
		cfg := small
		cfg.Exempt = func(tool string) bool { return tool == "tool_describe" }
		f := newFixture(t, newStore, cfg)
		big := strings.Repeat("e", 500)
		for name, m := range map[string]toolresult.Meta{
			"error":  {Tool: "test_tool", IsError: true},
			"exempt": {Tool: "tool_describe"},
		} {
			view, err := f.cache.Present(ctx, "s", m, big, 0)
			if err != nil || view.Cached || view.Content != big || view.Format != "complete" {
				t.Errorf("%s: %+v, %v", name, view, err)
			}
		}
		f.clock.advance(48 * time.Hour)
		if n, _ := f.cache.Purge(ctx); n != 0 {
			t.Fatal("a passed-through result was stored")
		}
	})

	t.Run("EmptyScope", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		big := strings.Repeat("x", 500)
		view, err := f.cache.Present(ctx, "", meta, big, 0)
		if !errors.Is(err, toolresult.ErrEmptyScope) || view.Content != big || view.Cached {
			t.Fatalf("Present: %+v, %v", view, err)
		}
		if _, err := f.cache.Put(ctx, "", meta, big); !errors.Is(err, toolresult.ErrEmptyScope) {
			t.Fatalf("Put: %v", err)
		}
		if _, err := f.cache.Read(ctx, "", "id", "", 0, 0, 0); !errors.Is(err, toolresult.ErrEmptyScope) {
			t.Fatalf("Read: %v", err)
		}
		if _, err := f.cache.Search(ctx, "", "id", "", "x", 0, 1, 0); !errors.Is(err, toolresult.ErrEmptyScope) {
			t.Fatalf("Search: %v", err)
		}
	})

	t.Run("StoreFailureReturnsBodyAndError", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		// The second call reuses the fixed id, so the store rejects it.
		f.cache = toolresult.New(f.store, toolresult.Config{HardCapBytes: 1000, DefaultBudget: 100, Now: f.clock.now, NewID: func() string { return "same" }})
		big := strings.Repeat("x", 500)
		if _, err := f.cache.Present(ctx, "s", meta, big, 0); err != nil {
			t.Fatal(err)
		}
		view, err := f.cache.Present(ctx, "s", meta, big, 0)
		if err == nil || view.Content != big || view.Cached || view.CacheID != "" || view.Footer != "" {
			t.Fatalf("want the unmodified body and an error with no pointer: %+v, %v", view, err)
		}
	})

	t.Run("JSONPointerReadsPreserveText", func(t *testing.T) {
		f := newFixture(t, newStore, toolresult.Config{})
		text := strings.Repeat("é🙂 source line\n", 700)
		raw := `{"stdout":` + jsonString(text) + `,"a/b~c":[9007199254740993]}`
		view, err := f.cache.Present(ctx, "owner", meta, raw, 4000)
		if err != nil || !view.Cached || view.Format != "json" {
			t.Fatalf("Present: %+v, %v", view, err)
		}
		var read strings.Builder
		for offset := 0; ; {
			page, readErr := f.cache.Read(ctx, "owner", view.CacheID, "/stdout", offset, 1<<29, 257)
			if readErr != nil || !utf8.ValidString(page.Content) || len(page.Content) > 257 || page.JSONPointer != "/stdout" {
				t.Fatalf("page: %+v, %v", page, readErr)
			}
			read.WriteString(page.Content)
			if !page.HasMore {
				break
			}
			if page.End <= offset {
				t.Fatal("page made no progress")
			}
			offset = page.End
		}
		if read.String() != text {
			t.Fatal("paging lost, duplicated or corrupted decoded stdout")
		}
		page, err := f.cache.Read(ctx, "owner", view.CacheID, "/a~1b~0c/0", 0, 0, 4000)
		if err != nil || page.Content != "9007199254740993" {
			t.Fatalf("pointer escaping or number precision: %+v %v", page, err)
		}
		for _, bad := range []string{"stdout", "/missing", "/stdout/0", "/a~2b", "/a~1b~0c/01"} {
			if _, err := f.cache.Read(ctx, "owner", view.CacheID, bad, 0, 0, 4000); err == nil {
				t.Errorf("invalid pointer %q succeeded", bad)
			}
		}
	})

	t.Run("SearchContinuesFromNextOffset", func(t *testing.T) {
		f := newFixture(t, newStore, toolresult.Config{})
		text := strings.Repeat("prefix ", 1000) + "NEEDLE one\nother\nNEEDLE two\nNEEDLE three\n"
		view, _ := f.cache.Present(ctx, "s", meta, `{"stdout":`+jsonString(text)+`}`, 4000)
		page, err := f.cache.Search(ctx, "s", view.CacheID, "/stdout", "NEEDLE", 0, 1, 4000)
		if err != nil || !page.HasMore || len(page.Matches) != 1 || page.JSONPointer != "/stdout" {
			t.Fatalf("first search: %+v %v", page, err)
		}
		match := page.Matches[0]
		if !match.ContextTruncated || !strings.Contains(match.Context, "NEEDLE one") {
			t.Fatalf("long-line context: %+v", match)
		}
		read, err := f.cache.Read(ctx, "s", view.CacheID, "/stdout", match.MatchOffset, match.MatchEnd-match.MatchOffset, 4000)
		if err != nil || read.Content != "NEEDLE" {
			t.Fatalf("search coordinates did not fetch the match: %+v %v", read, err)
		}
		next, err := f.cache.Search(ctx, "s", view.CacheID, "/stdout", "NEEDLE", page.NextOffset, 20, 4000)
		if err != nil || next.HasMore || len(next.Matches) != 2 || next.Matches[0].MatchOffset <= match.MatchOffset {
			t.Fatalf("continued search: %+v %v", next, err)
		}
	})

	t.Run("SearchLines", func(t *testing.T) {
		f := newFixture(t, newStore, toolresult.Config{HardCapBytes: 5000, DefaultBudget: 100})
		lines := []string{"line 1: hello world", "line 2: foo bar", "line 3: hello again", "line 4: nothing here", "line 5: hello final"}
		body := strings.Repeat("x", 50) + "\n" + strings.Join(lines, "\n") + "\n" + strings.Repeat("y", 50)
		view, _ := f.cache.Present(ctx, "s", meta, body, 0)
		res, err := f.cache.Search(ctx, "s", view.CacheID, "", "hello", 0, 10, 4000)
		if err != nil || len(res.Matches) != 3 || res.Matches[0].Line != 2 {
			t.Fatalf("Search: %+v, %v", res, err)
		}
		if _, err := f.cache.Search(ctx, "s", view.CacheID, "", "(", 0, 10, 0); err == nil {
			t.Fatal("invalid pattern accepted")
		}
	})

	t.Run("HandlersUseSessionScopeOnly", func(t *testing.T) {
		f := newFixture(t, newStore, small)
		view, _ := f.cache.Present(ctx, "owner", meta, strings.Repeat("needle\n", 60), 0)
		in := map[string]any{"id": view.CacheID, "scope": "owner", "caller_id": "owner"}
		text, isErr := f.cache.HandleFetch(ctx, "intruder", in, 100)
		if !isErr || !strings.Contains(text, "not found or expired") {
			t.Fatalf("intruder fetch: %q", text)
		}
		text, isErr = f.cache.HandleSearch(ctx, "intruder", map[string]any{"id": view.CacheID, "pattern": "needle"}, 100)
		if !isErr {
			t.Fatalf("intruder search: %q", text)
		}
		if _, isErr = f.cache.HandleFetch(ctx, "owner", map[string]any{"id": view.CacheID}, 100); isErr {
			t.Fatal("owner fetch failed")
		}
	})
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
