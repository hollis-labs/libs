package toolresult_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/memstore"
)

var (
	ulidPattern    = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)
	expiresPattern = regexp.MustCompile(`expires_at=[0-9TZ:-]+`)
)

func normalize(s string) string {
	return expiresPattern.ReplaceAllString(ulidPattern.ReplaceAllString(s, "<ID>"), "expires_at=<EXPIRES>")
}

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name)) //nolint:gosec // fixed testdata directory
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestGoldenParityWithNanite is acceptance criterion (b): Present,
// HandleFetch and HandleSearch emit the same bytes as Nanite's code did for
// the same inputs (testdata/golden/README.txt says how the files were made).
func TestGoldenParityWithNanite(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	ctx := context.Background()
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			cache := toolresult.New(memstore.New(), toolresult.Config{
				HardCapBytes: c.hardCap, TTL: time.Hour, NewID: func() string { return id },
				Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
			})
			view, err := cache.Present(ctx, "s", toolresult.Meta{Tool: "tool", CallID: "call"}, c.body, c.budget)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := normalize(view.Content), golden(t, c.name+".present.txt"); got != want {
				t.Errorf("Present differs from Nanite:\n got: %q\nwant: %q", got, want)
			}
			sub := func(in map[string]any) map[string]any {
				m := map[string]any{}
				for k, v := range in {
					if v == "$ID" {
						v = view.CacheID
					}
					m[k] = v
				}
				return m
			}
			var b strings.Builder
			for i, in := range c.fetch {
				text, isErr := cache.HandleFetch(ctx, "s", sub(in), c.budget)
				fmt.Fprintf(&b, "=== fetch %d error=%t\n%s\n", i, isErr, text)
			}
			if len(c.fetch) > 0 {
				if got, want := normalize(b.String()), golden(t, c.name+".fetch.txt"); got != want {
					t.Errorf("HandleFetch differs from Nanite:\n got: %q\nwant: %q", got, want)
				}
			}
			b.Reset()
			for i, in := range c.search {
				text, isErr := cache.HandleSearch(ctx, "s", sub(in), c.budget)
				fmt.Fprintf(&b, "=== search %d error=%t\n%s\n", i, isErr, text)
			}
			if len(c.search) > 0 {
				if got, want := normalize(b.String()), golden(t, c.name+".search.txt"); got != want {
					t.Errorf("HandleSearch differs from Nanite:\n got: %q\nwant: %q", got, want)
				}
			}
		})
	}
}
