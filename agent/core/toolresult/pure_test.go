package toolresult_test

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	toolresult "github.com/hollis-labs/go-toolresult"
)

func TestPreviewTaskFieldsAndCommentEnds(t *testing.T) {
	comments := []any{}
	for range 8 {
		comments = append(comments, map[string]any{"content": strings.Repeat("historical investigation ", 200)})
	}
	comments[7] = map[string]any{"content": "DEPLOYMENT VERIFIED; current code fixes discovery"}
	body, _ := json.MarshalIndent(map[string]any{"ok": true, "data": map[string]any{
		"id": "task-example", "title": "Repair discovery", "status": "review", "comments": comments,
		"description": strings.Repeat("Background detail. ", 700),
	}}, "", "  ")
	text, format := toolresult.Preview(string(body), 4000)
	if format != "json" || len(text) > 4000 {
		t.Fatalf("format %q, %d bytes", format, len(text))
	}
	for _, want := range []string{"Repair discovery", "review", "DEPLOYMENT VERIFIED", "OMITTED items", `"/data/comments/7/content"`} {
		if !strings.Contains(text, want) {
			t.Errorf("preview lost %q:\n%s", want, text)
		}
	}
}

func TestPreviewLongTextAndPythonShape(t *testing.T) {
	text := "FIRST SOURCE\n" + strings.Repeat("é", 40000) + "\nLAST SOURCE"
	view, format := toolresult.Preview(text, 4000)
	if format != "text" || !strings.Contains(view, "FIRST SOURCE") || !strings.Contains(view, "LAST SOURCE") ||
		!strings.Contains(view, "OMITTED bytes") || !utf8.ValidString(view) || len(view) > 4000 {
		t.Fatalf("invalid text preview (%s): %s", format, view)
	}
	body, _ := json.Marshal(map[string]any{"stdout": text, "stderr": "diagnostic", "result": nil, "error": "scan stopped"})
	view, format = toolresult.Preview(string(body), 4000)
	for _, want := range []string{"FIRST SOURCE\n", "LAST SOURCE", "scan stopped", "diagnostic", "/stdout"} {
		if !strings.Contains(view, want) {
			t.Errorf("preview lost %q: %s", want, view)
		}
	}
	if format != "json" || len(view) > 4000 {
		t.Fatalf("unexpected format or budget: %s / %d", format, len(view))
	}
}

func TestPreviewScalarJSONIsText(t *testing.T) {
	for _, body := range []string{`"a string"`, `12`, `null`, `true`, `{bad`} {
		if _, format := toolresult.Preview(body, 500); format != "text" {
			t.Errorf("%q: format %q, want text", body, format)
		}
	}
}

func TestBudgetForWindow(t *testing.T) {
	var o toolresult.BudgetOptions
	for _, tc := range []struct{ window, want int }{
		{0, 4000}, {-5, 4000}, {200_000, 4000}, {1_000_000, 16000}, {10_000_000, 32000}, {250_000, 4000}, {300_000, 4800},
	} {
		if got := toolresult.BudgetForWindow(tc.window, o); got != tc.want {
			t.Errorf("window %d = %d, want %d", tc.window, got, tc.want)
		}
	}
	custom := toolresult.BudgetOptions{Floor: 100, Ceiling: 500, BytesPerToken: 2, Fraction: 0.5}
	if got := toolresult.BudgetForWindow(300, custom); got != 300 {
		t.Errorf("custom = %d, want 300", got)
	}
	if got := toolresult.BudgetForWindow(0, custom); got != 100 {
		t.Errorf("custom unknown window = %d, want 100", got)
	}
}

func TestSelect(t *testing.T) {
	body := `{"a/b":{"c~d":[10,"x\u00e9y",null,{"k":1.50}]},"s":"hi\nthere","n":null,"big":9007199254740993,"e":{},"l":[]}`
	for _, tc := range []struct{ ptr, want string }{
		{"", body},
		{"/a~1b/c~0d/0", "10"},
		{"/a~1b/c~0d/1", "xéy"},
		{"/a~1b/c~0d/2", "null"},
		{"/a~1b/c~0d/3", `{"k":1.50}`},
		{"/a~1b/c~0d/3/k", "1.50"},
		{"/s", "hi\nthere"},
		{"/n", "null"},
		{"/big", "9007199254740993"},
	} {
		got, err := toolresult.Select(body, tc.ptr)
		if err != nil || got != tc.want {
			t.Errorf("Select(%q) = %q, %v; want %q", tc.ptr, got, err, tc.want)
		}
	}
	for _, ptr := range []string{
		"a", "/missing", "/s/0", "/n/x", "/big/0", "/a~1b/c~0d/01", "/a~1b/c~0d/-0", "/a~1b/c~0d/-1",
		"/a~1b/c~0d/4", "/a~1b/c~0d/x", "/a~1b/c~0d/", "/a~2b", "/a~", "/e/x", "/l/0", "/a~1b/c~0d/+1",
	} {
		if got, err := toolresult.Select(body, ptr); err == nil {
			t.Errorf("Select(%q) = %q, want an error", ptr, got)
		}
	}
	if _, err := toolresult.Select("plain text", "/x"); err == nil {
		t.Error("pointer into non-JSON succeeded")
	}
	if got, err := toolresult.Select("plain text", ""); err != nil || got != "plain text" {
		t.Errorf("empty pointer on text: %q, %v", got, err)
	}
	// A key that is the empty string is addressed by a trailing slash.
	if got, err := toolresult.Select(`{"":7}`, "/"); err != nil || got != "7" {
		t.Errorf(`Select("/") = %q, %v`, got, err)
	}
}

func TestReadPageTilesTheBodyExactly(t *testing.T) {
	body := strings.Repeat("é🙂 a\n", 300) + "日本語"
	for _, length := range []int{1, 2, 3, 4, 5, 7, 64, 257} {
		var got strings.Builder
		for offset, guard := 0, 0; ; guard++ {
			page, err := toolresult.ReadPage(body, offset, length, 257)
			if err != nil || !utf8.ValidString(page.Content) || page.Content != body[page.Offset:page.End] {
				t.Fatalf("length %d: %+v, %v", length, page, err)
			}
			if page.End <= offset && page.HasMore || guard > len(body) {
				t.Fatalf("length %d: no progress at %d", length, offset)
			}
			got.WriteString(page.Content)
			if !page.HasMore {
				break
			}
			offset = page.End
		}
		if got.String() != body {
			t.Fatalf("length %d: pages do not tile the body", length)
		}
	}
	if _, err := toolresult.ReadPage(body, -1, 0, 10); err == nil {
		t.Error("negative offset accepted")
	}
	page, err := toolresult.ReadPage(body, len(body)+50, 0, 10)
	if err != nil || page.Content != "" || page.HasMore || page.Offset != len(body) {
		t.Errorf("offset past the end: %+v, %v", page, err)
	}
	// An offset inside a rune moves back to the rune start.
	if page, _ := toolresult.ReadPage("é", 1, 10, 10); page.Offset != 0 || page.Content != "é" {
		t.Errorf("mid-rune offset: %+v", page)
	}
	// Length smaller than the rune still makes progress.
	if page, _ := toolresult.ReadPage("🙂x", 0, 1, 10); page.Content != "🙂" || !page.HasMore {
		t.Errorf("tiny length: %+v", page)
	}
}

func TestSearchPageContinuationNeverRepeats(t *testing.T) {
	var b strings.Builder
	for i := range 250 {
		b.WriteString("row hit ")
		b.WriteString(strings.Repeat("é", i%7))
		b.WriteString("\nmiss\n")
	}
	body := b.String()
	seen := map[int]bool{}
	total := 0
	for offset, guard := 0, 0; ; guard++ {
		res, err := toolresult.SearchPage(body, "hit", offset, 30, 4000)
		if err != nil || guard > 100 {
			t.Fatalf("SearchPage: %v (guard %d)", err, guard)
		}
		for _, m := range res.Matches {
			if seen[m.MatchOffset] {
				t.Fatalf("match at %d repeated", m.MatchOffset)
			}
			seen[m.MatchOffset] = true
			total++
			if body[m.MatchOffset:m.MatchEnd] != "hit" || !utf8.ValidString(m.Context) || m.Context != body[m.ContextOffset:m.ContextEnd] {
				t.Fatalf("bad match %+v", m)
			}
		}
		if !res.HasMore {
			if res.NextOffset != len(body) {
				t.Fatalf("final NextOffset = %d, want %d", res.NextOffset, len(body))
			}
			break
		}
		if res.NextOffset <= offset {
			t.Fatalf("NextOffset %d does not advance past %d", res.NextOffset, offset)
		}
		offset = res.NextOffset
	}
	if total != 250 {
		t.Fatalf("found %d matches across pages, want 250", total)
	}
	if _, err := toolresult.SearchPage(body, "(", 0, 1, 1000); err == nil {
		t.Error("bad regexp accepted")
	}
	if _, err := toolresult.SearchPage(body, "x", -1, 1, 1000); err == nil {
		t.Error("negative offset accepted")
	}
	if res, err := toolresult.SearchPage(body, "hit", len(body)+5, 1, 1000); err != nil || len(res.Matches) != 0 || res.HasMore {
		t.Errorf("offset past end: %+v, %v", res, err)
	}
	res, _ := toolresult.SearchPage(body, "hit", 0, 1000, 1<<20)
	if len(res.Matches) != 100 {
		t.Errorf("maxMatches cap: got %d matches, want 100", len(res.Matches))
	}
}

func TestCutUTF8(t *testing.T) {
	for _, tc := range []struct {
		in    string
		n     int
		want  string
		trunc bool
	}{
		{"héllo", 2, "h", true}, {"héllo", 3, "hé", true}, {"héllo", 99, "héllo", false},
		{"héllo", 0, "", true}, {"héllo", -3, "", true}, {"", 5, "", false}, {"🙂", 3, "", true}, {"🙂", 4, "🙂", false},
	} {
		got, trunc := toolresult.CutUTF8(tc.in, tc.n)
		if got != tc.want || trunc != tc.trunc {
			t.Errorf("CutUTF8(%q, %d) = %q, %v; want %q, %v", tc.in, tc.n, got, trunc, tc.want, tc.trunc)
		}
	}
}

// randomText builds valid UTF-8 mixing ASCII, newlines and 2-4 byte runes.
func randomText(r *rand.Rand, n int) string {
	pool := []rune{'a', 'b', ' ', '\n', 'é', 'ß', '日', '本', '🙂', '𝄞', '"', '\\'}
	var b strings.Builder
	for range n {
		b.WriteRune(pool[r.IntN(len(pool))])
	}
	return b.String()
}

// TestCutUTF8Property: for random valid text and random limits the head is a
// valid prefix within the limit and is maximal.
func TestCutUTF8Property(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test data, not security
	for range 2000 {
		s := randomText(r, r.IntN(60))
		n := r.IntN(len(s) + 6)
		head, trunc := toolresult.CutUTF8(s, n)
		if !strings.HasPrefix(s, head) || len(head) > n || !utf8.ValidString(head) || trunc != (len(head) < len(s)) {
			t.Fatalf("CutUTF8(%q, %d) = %q, %v", s, n, head, trunc)
		}
		if len(head) < len(s) && len(head) < n {
			if _, w := utf8.DecodeRuneInString(s[len(head):]); len(head)+w <= n {
				t.Fatalf("CutUTF8(%q, %d) = %q is not maximal", s, n, head)
			}
		}
	}
}

// TestPagingProperty: random valid text, random page sizes, both ReadPage
// tiling and Preview's bound hold.
func TestPagingProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // deterministic test data, not security
	for range 500 {
		body := randomText(r, r.IntN(400))
		length := 1 + r.IntN(40)
		var got strings.Builder
		for offset := 0; ; {
			page, err := toolresult.ReadPage(body, offset, length, 4+r.IntN(30))
			if err != nil || !utf8.ValidString(page.Content) {
				t.Fatalf("page %+v, %v", page, err)
			}
			got.WriteString(page.Content)
			if !page.HasMore {
				break
			}
			if page.End <= offset {
				t.Fatal("no progress")
			}
			offset = page.End
		}
		if got.String() != body {
			t.Fatalf("tiling failed for %q", body)
		}
		budget := r.IntN(600)
		prev, _ := toolresult.Preview(body, budget)
		if len(prev) > budget || !utf8.ValidString(prev) {
			t.Fatalf("Preview(%d) = %d bytes, valid=%v", budget, len(prev), utf8.ValidString(prev))
		}
	}
}

func FuzzReadPage(f *testing.F) {
	f.Add("héllo 🙂 wörld\nline", 3, 4, 8)
	f.Add("🙂🙂🙂", 1, 1, 4)
	f.Add("", 0, 0, 0)
	f.Add("\xff\xfe bad \xc3", 2, 3, 5)
	f.Fuzz(func(t *testing.T, body string, offset, length, budget int) {
		page, err := toolresult.ReadPage(body, offset, length, budget)
		if offset < 0 {
			if err == nil {
				t.Fatal("negative offset accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if page.Offset < 0 || page.Offset > page.End || page.End > len(body) || page.Content != body[page.Offset:page.End] {
			t.Fatalf("incoherent page %+v for %d-byte body", page, len(body))
		}
		if page.HasMore != (page.End < len(body)) || page.TotalBytes != len(body) {
			t.Fatalf("bad flags %+v", page)
		}
		if page.HasMore && page.End == 0 {
			t.Fatal("no progress from offset 0")
		}
		if page.HasMore || page.Offset < len(body) {
			if page.End == page.Offset && page.Offset < len(body) {
				t.Fatal("empty page before the end")
			}
		}
		if utf8.ValidString(body) && !utf8.ValidString(page.Content) {
			t.Fatalf("split a rune: %q", page.Content)
		}
		if limit := max(4, budget); len(page.Content) > limit {
			t.Fatalf("page %d bytes exceeds budget %d", len(page.Content), limit)
		}
	})
}

func FuzzSelect(f *testing.F) {
	body := `{"a":[1,"é",{"b/c":null}],"~":"x"}`
	for _, p := range []string{"", "/a/0", "/a/1", "/a/2/b~1c", "/~0", "/a/01", "/a~2", "x", "/", "//", "/a/-1"} {
		f.Add(body, p)
	}
	f.Add("not json", "/a")
	f.Fuzz(func(t *testing.T, body, pointer string) {
		got, err := toolresult.Select(body, pointer)
		if err == nil && pointer == "" && got != body {
			t.Fatal("empty pointer changed the body")
		}
		if err == nil && pointer != "" && utf8.ValidString(body) && !utf8.ValidString(got) {
			t.Fatalf("Select produced invalid UTF-8 from valid input: %q", got)
		}
	})
}

func FuzzPreview(f *testing.F) {
	f.Add(`{"id":"x","items":[1,2,3,4,5],"text":"`+strings.Repeat("é", 300)+`"}`, 300)
	f.Add(strings.Repeat("line\n", 200), 150)
	f.Add(`[{"a":{"b":{"c":"d"}}}]`, 40)
	f.Add("🙂", 0)
	f.Add("\xff\xfe", 5)
	f.Add(`"str"`, 3)
	f.Fuzz(func(t *testing.T, body string, budget int) {
		text, format := toolresult.Preview(body, budget)
		if format != "json" && format != "text" {
			t.Fatalf("format %q", format)
		}
		if budget >= 0 && len(text) > budget {
			t.Fatalf("preview is %d bytes for budget %d: %q", len(text), budget, text)
		}
		if budget < 0 && len(text) > len(body) {
			t.Fatalf("negative budget grew the text")
		}
		if utf8.ValidString(body) && !utf8.ValidString(text) {
			t.Fatalf("preview split a rune: %q", text)
		}
	})
}

func FuzzSearchPage(f *testing.F) {
	f.Add("a hit\nmiss\nhit again\n", "hit", 0, 1, 512)
	f.Add("🙂 hit 🙂", "hit", 3, 5, 100)
	f.Fuzz(func(t *testing.T, body, pattern string, offset, maxMatches, budget int) {
		res, err := toolresult.SearchPage(body, pattern, offset, maxMatches, budget)
		if err != nil {
			return
		}
		if len(res.Matches) > 100 || res.NextOffset < 0 || res.NextOffset > len(body) || res.TotalBytes != len(body) {
			t.Fatalf("incoherent result %+v", res)
		}
		if res.HasMore && res.NextOffset < offset {
			t.Fatalf("NextOffset %d before offset %d", res.NextOffset, offset)
		}
		for _, m := range res.Matches {
			if m.ContextOffset < 0 || m.ContextOffset > m.ContextEnd || m.ContextEnd > len(body) || m.Context != body[m.ContextOffset:m.ContextEnd] {
				t.Fatalf("bad context %+v", m)
			}
			if utf8.ValidString(body) && !utf8.ValidString(m.Context) {
				t.Fatalf("context split a rune: %q", m.Context)
			}
		}
	})
}
