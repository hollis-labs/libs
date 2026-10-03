package anthropicwire

import (
	"encoding/json"
	"sort"

	chatstream "github.com/hollis-labs/go-chatstream"
)

// usageAcc merges Anthropic's usage objects field by field. message_start
// carries the initial counts and every message_delta carries cumulative ones,
// but message_delta may leave the input counts out, so a missing field keeps
// its earlier value instead of resetting to zero.
type usageAcc struct {
	seen         bool
	in, cw, cr   int
	out          int
	thinking     int
	hasThinking  bool
	serverTool   map[string]int
	hasServerUse bool
}

type wireUsage struct {
	Input   *int `json:"input_tokens"`
	Output  *int `json:"output_tokens"`
	CacheW  *int `json:"cache_creation_input_tokens"`
	CacheR  *int `json:"cache_read_input_tokens"`
	Details *struct {
		Thinking *int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	ServerToolUse map[string]json.RawMessage `json:"server_tool_use"`
}

func (a *usageAcc) merge(raw json.RawMessage) {
	var w wireUsage
	if json.Unmarshal(raw, &w) != nil {
		return
	}
	set := func(dst *int, src *int) {
		if src != nil {
			*dst = *src
			a.seen = true
		}
	}
	set(&a.in, w.Input)
	set(&a.out, w.Output)
	set(&a.cw, w.CacheW)
	set(&a.cr, w.CacheR)
	if w.Details != nil && w.Details.Thinking != nil {
		a.thinking, a.hasThinking = *w.Details.Thinking, true
		a.seen = true
	}
	for k, v := range w.ServerToolUse {
		var n int
		if json.Unmarshal(v, &n) == nil {
			if a.serverTool == nil {
				a.serverTool = map[string]int{}
			}
			a.serverTool[k] = n
			a.hasServerUse, a.seen = true, true
		}
	}
}

// build converts the accumulated counts to chatstream's disjoint components.
// Anthropic's input_tokens excludes cache reads and writes, so they map one to
// one; a reported thinking count is moved out of Output into Reasoning.
func (a *usageAcc) build(scope chatstream.UsageScope) (chatstream.Usage, bool) {
	if !a.seen {
		return chatstream.Usage{}, false
	}
	u, err := chatstream.UsageFromExclusiveInput(scope, a.in, a.cr, a.cw, a.out)
	if err != nil {
		return chatstream.Usage{}, false
	}
	if a.hasThinking && a.thinking > 0 && a.thinking <= u.Output {
		u.Output -= a.thinking
		u.Reasoning = a.thinking
	}
	if a.hasServerUse {
		keys := make([]string, 0, len(a.serverTool))
		for k := range a.serverTool {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		u.Extra = map[string]int{}
		for _, k := range keys {
			u.Extra["server_tool_use."+k] = a.serverTool[k]
		}
	}
	return u, true
}
