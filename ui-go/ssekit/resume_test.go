package ssekit_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	ssekit "github.com/hollis-labs/go-ssekit"
)

func req(url, lastEventID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, url, nil)
	if lastEventID != "" {
		r.Header.Set("Last-Event-ID", lastEventID)
	}
	return r
}

func numeric(a, b string) int {
	x, _ := strconv.Atoi(a)
	y, _ := strconv.Atoi(b)
	return x - y
}

func TestResumeCursor(t *testing.T) {
	keys := ssekit.WithQueryKeys("from", "after", "since_seq")
	cases := []struct {
		name   string
		r      *http.Request
		o      []ssekit.ResumeOption
		want   string
		wantOK bool
	}{
		{"nothing", req("/s", ""), []ssekit.ResumeOption{keys}, "", false},
		{"header only", req("/s", "7"), []ssekit.ResumeOption{keys}, "7", true},
		{"query only", req("/s?from=3", ""), []ssekit.ResumeOption{keys}, "3", true},
		{"query ignored without keys", req("/s?from=3", ""), nil, "", false},
		{"header without keys", req("/s?from=3", "9"), nil, "9", true},
		{"key order: first non-empty key wins", req("/s?after=2&from=5&since_seq=9", ""), []ssekit.ResumeOption{keys}, "5", true},
		{"empty first key falls through", req("/s?from=&after=2", ""), []ssekit.ResumeOption{keys}, "2", true},
		{"values trimmed", req("/s?from=%204%20", " 8 "), []ssekit.ResumeOption{keys, ssekit.WithPrecedence(ssekit.QueryFirst)}, "4", true},
		{"whitespace-only header is absent", req("/s?from=3", "   "), []ssekit.ResumeOption{keys}, "3", true},
		{"default precedence is header first", req("/s?from=3", "9"), []ssekit.ResumeOption{keys}, "9", true},
		{"header first, header smaller", req("/s?from=30", "9"), []ssekit.ResumeOption{keys, ssekit.WithPrecedence(ssekit.HeaderFirst)}, "9", true},
		{"query first", req("/s?from=3", "9"), []ssekit.ResumeOption{keys, ssekit.WithPrecedence(ssekit.QueryFirst)}, "3", true},
		{"newest: header newer", req("/s?from=3", "9"), []ssekit.ResumeOption{keys, ssekit.WithPrecedence(ssekit.Newest(numeric))}, "9", true},
		{"newest: query newer", req("/s?from=30", "9"), []ssekit.ResumeOption{keys, ssekit.WithPrecedence(ssekit.Newest(numeric))}, "30", true},
		{"newest: tie goes to header", req("/s?from=9", "9"), []ssekit.ResumeOption{keys, ssekit.WithPrecedence(ssekit.Newest(numeric))}, "9", true},
		{"opaque cursors are not parsed", req("/s", "abc:def"), nil, "abc:def", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ssekit.ResumeCursor(c.r, c.o...)
			if got != c.want || ok != c.wantOK {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}
