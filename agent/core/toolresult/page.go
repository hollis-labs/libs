package toolresult

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Select returns the value at an RFC 6901 JSON pointer inside body. An empty
// pointer selects body unchanged. A string value is returned decoded (so
// "/stdout" yields the text, not a quoted JSON literal); objects, arrays,
// numbers, booleans and null are returned as their original JSON bytes, which
// keeps large numbers exact.
//
// Array indices must be canonical decimal ("01" and "-0" are rejected), and
// "~0" / "~1" unescape to "~" and "/". Select fails when the pointer does not
// start with "/", when body is not JSON, or when the pointer names a missing
// field, a bad index or a path through a scalar.
func Select(body, jsonPointer string) (string, error) {
	if jsonPointer == "" {
		return body, nil
	}
	if !strings.HasPrefix(jsonPointer, "/") {
		return "", errors.New("json_pointer must be empty or start with /")
	}
	if !json.Valid([]byte(body)) {
		return "", errors.New("cached result is not JSON; omit json_pointer")
	}
	raw := json.RawMessage(body)
	for _, part := range strings.Split(jsonPointer[1:], "/") {
		for i := 0; i < len(part); i++ {
			if part[i] == '~' {
				if i+1 >= len(part) || (part[i+1] != '0' && part[i+1] != '1') {
					return "", errors.New("invalid JSON pointer escape")
				}
				i++
			}
		}
		key := strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) == nil && object != nil {
			var ok bool
			raw, ok = object[key]
			if !ok {
				return "", fmt.Errorf("JSON pointer %q: field %q not found", jsonPointer, key)
			}
			continue
		}
		var array []json.RawMessage
		if json.Unmarshal(raw, &array) == nil && array != nil {
			index, parseErr := strconv.Atoi(key)
			if parseErr != nil || index < 0 || index >= len(array) || strconv.Itoa(index) != key {
				return "", fmt.Errorf("JSON pointer %q: invalid array index %q", jsonPointer, key)
			}
			raw = array[index]
			continue
		}
		return "", fmt.Errorf("JSON pointer %q traverses a scalar", jsonPointer)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && string(raw) != "null" {
		return text, nil
	}
	return string(raw), nil
}

// Page is one window over a value. Offsets are UTF-8 byte offsets into the
// selected value (see [Select]) and are end-exclusive.
type Page struct {
	// Content is body[Offset:End].
	Content string
	// Offset is the actual start, moved back to a rune boundary if needed.
	Offset int
	// End is the actual end, and the offset to continue from when HasMore.
	End int
	// TotalBytes is the length of the whole selected value.
	TotalBytes int
	// HasMore reports whether bytes remain after End.
	HasMore bool
	// JSONPointer echoes the pointer the page was read through. [ReadPage]
	// leaves it empty; [Cache.Read] fills it in.
	JSONPointer string
}

// ReadPage returns up to length bytes of body starting at offset. length is
// capped by budget (and defaults to budget when zero or negative); budget is
// raised to at least 4 so that any single rune fits. Both edges are moved to
// rune boundaries, so the concatenation of consecutive pages, each started at
// the previous End, reproduces body exactly and every page of valid UTF-8 is
// valid UTF-8. ReadPage always makes progress: a page that would be empty
// because length is smaller than one rune returns that whole rune instead.
func ReadPage(body string, offset, length, budget int) (Page, error) {
	if offset < 0 {
		return Page{}, errors.New("offset must be non-negative")
	}
	budget = max(4, budget)
	if length <= 0 || length > budget {
		length = budget
	}
	start := min(offset, len(body))
	for start > 0 && start < len(body) && !utf8.RuneStart(body[start]) {
		start--
	}
	end := start + min(length, len(body)-start)
	for end > start && end < len(body) && !utf8.RuneStart(body[end]) {
		end--
	}
	if end == start && start < len(body) {
		_, width := utf8.DecodeRuneInString(body[start:])
		end += width // Always make progress, even if length split one rune.
	}
	return Page{Content: body[start:end], Offset: start, End: end, TotalBytes: len(body), HasMore: end < len(body)}, nil
}

// SearchMatch is one matching line with bounded surrounding context. All
// offsets are byte offsets into the searched value and can be passed to
// [ReadPage] unchanged.
type SearchMatch struct {
	// Line is the 1-based line number of the match.
	Line int
	// MatchOffset and MatchEnd bound the matched text.
	MatchOffset, MatchEnd int
	// ContextOffset and ContextEnd bound Context.
	ContextOffset, ContextEnd int
	// Context is up to two lines before and after the match, clipped to fit
	// the budget.
	Context string
	// ContextTruncated reports that Context was clipped.
	ContextTruncated bool
}

// SearchResult is one page of search results.
type SearchResult struct {
	// Matches are the matching lines, in order.
	Matches []SearchMatch
	// TotalBytes is the length of the whole searched value.
	TotalBytes int
	// HasMore reports that further matching lines were left out.
	HasMore bool
	// NextOffset is where to continue: pass it as offset to get the next
	// page. It equals TotalBytes when nothing remains.
	NextOffset int
	// JSONPointer echoes the pointer searched through; [Cache.Search] fills
	// it in.
	JSONPointer string
}

// SearchPage returns the lines of body matching the RE2 pattern, starting at
// byte offset. At most maxMatches lines (clamped to 1..100) are returned, and
// the context text is bounded by budget (raised to at least 512). When more
// matches remain, HasMore is set and NextOffset is the offset to continue
// from; continuation never repeats a match.
func SearchPage(body, pattern string, offset, maxMatches, budget int) (SearchResult, error) {
	if offset < 0 {
		return SearchResult{}, errors.New("offset must be non-negative")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return SearchResult{}, fmt.Errorf("invalid search pattern: %w", err)
	}
	maxMatches = min(100, max(1, maxMatches))
	budget = max(512, budget)
	out := SearchResult{TotalBytes: len(body), NextOffset: len(body)}
	if offset >= len(body) {
		return out, nil
	}
	lines := strings.SplitAfter(body, "\n")
	starts := make([]int, len(lines)+1)
	for i, line := range lines {
		starts[i+1] = starts[i] + len(line)
	}
	remaining := budget - 256
	for i, line := range lines {
		start := starts[i]
		if starts[i+1] <= offset {
			continue
		}
		from := min(len(line), max(0, offset-start))
		match := re.FindStringIndex(strings.TrimSuffix(line[from:], "\n"))
		if match == nil {
			continue
		}
		if len(out.Matches) >= maxMatches || remaining < 256 {
			out.HasMore, out.NextOffset = true, start+from
			break
		}
		matchStart, matchEnd := start+from+match[0], start+from+match[1]
		contextStart := starts[max(0, i-2)]
		contextEnd := starts[min(len(lines), i+3)]
		allowance := min(2000, remaining-200)
		clipped := contextEnd-contextStart > allowance
		if clipped {
			contextStart = max(contextStart, matchStart-allowance/3)
			contextEnd = min(contextEnd, contextStart+allowance)
		}
		for contextStart < contextEnd && !utf8.RuneStart(body[contextStart]) {
			contextStart++
		}
		for contextEnd > contextStart && contextEnd < len(body) && !utf8.RuneStart(body[contextEnd]) {
			contextEnd--
		}
		text := body[contextStart:contextEnd]
		out.Matches = append(out.Matches, SearchMatch{Line: i + 1, MatchOffset: matchStart, MatchEnd: matchEnd, ContextOffset: contextStart, ContextEnd: contextEnd, Context: text, ContextTruncated: clipped})
		remaining -= len(text) + 200
	}
	return out, nil
}
