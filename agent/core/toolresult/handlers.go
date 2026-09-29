package toolresult

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// HandleFetch executes one call of the fetch tool for scope. input is the
// model's decoded arguments (id, json_pointer, offset, length); budget is the
// caller's current result budget. It returns the text to hand back to the
// model and whether it is an error. The header text is prompt surface and is
// deliberately identical to Nanite's:
//
//	[Cached result <id>; json_pointer="..."; bytes a..b of N (end-exclusive); has_more=t; next_offset=b]
//
// scope must be host-derived; never read it from input.
func (c *Cache) HandleFetch(ctx context.Context, scope string, input map[string]any, budget int) (text string, isError bool) {
	id, _ := input["id"].(string)
	if id == "" {
		return "Error: 'id' is required", true
	}
	pointer, _ := input["json_pointer"].(string)
	offset, err := cacheInt(input, "offset", 0)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	length, err := cacheInt(input, "length", budget)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	page, err := c.Read(ctx, scope, id, pointer, offset, length, budget)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), true
	}
	header := fmt.Sprintf("[Cached result %s; json_pointer=%q; bytes %d..%d of %d (end-exclusive); has_more=%t; next_offset=%d]\n\n",
		id, pointer, page.Offset, page.End, page.TotalBytes, page.HasMore, page.End)
	return header + page.Content, false
}

// HandleSearch executes one call of the search tool for scope. input holds
// id, pattern, json_pointer, offset and max_matches (default 20). See
// [Cache.HandleFetch] for the contract.
func (c *Cache) HandleSearch(ctx context.Context, scope string, input map[string]any, budget int) (text string, isError bool) {
	id, _ := input["id"].(string)
	pattern, _ := input["pattern"].(string)
	if id == "" || pattern == "" {
		return "Error: 'id' and 'pattern' are required", true
	}
	pointer, _ := input["json_pointer"].(string)
	offset, err := cacheInt(input, "offset", 0)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	maximum, err := cacheInt(input, "max_matches", 20)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	page, err := c.Search(ctx, scope, id, pointer, pattern, offset, maximum, budget)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), true
	}
	var out strings.Builder
	fmt.Fprintf(&out, "[Cached result %s; json_pointer=%q; matching lines=%d; total_bytes=%d; has_more=%t; next_offset=%d]\n", id, pointer, len(page.Matches), page.TotalBytes, page.HasMore, page.NextOffset)
	for _, m := range page.Matches {
		fmt.Fprintf(&out, "\n--- Line %d; match bytes %d..%d; context bytes %d..%d; context_truncated=%t ---\n%s\n", m.Line, m.MatchOffset, m.MatchEnd, m.ContextOffset, m.ContextEnd, m.ContextTruncated, m.Context)
	}
	if len(page.Matches) == 0 {
		out.WriteString("No matching lines.\n")
	}
	return out.String(), false
}

// cacheInt reads a non-negative integer argument. JSON decoders yield
// float64 (or json.Number); Go callers may pass int or int64.
func cacheInt(input map[string]any, key string, fallback int) (int, error) {
	value, exists := input[key]
	if !exists || value == nil {
		return fallback, nil
	}
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case int:
		number = float64(v)
	case int64:
		number = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s must be a non-negative integer", key)
		}
		number = f
	default:
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	if number < 0 || number > float64(1<<30) || number != float64(int(number)) {
		return 0, fmt.Errorf("%s must be a non-negative integer no greater than 1073741824", key)
	}
	return int(number), nil
}
