package agenthost

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/hollis-labs/libs/agent/core/turn"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

// Compaction keeps a long thread inside the model's window. When the last request of a
// thread was larger than Config.CompactAt tokens, the older part of the thread is put to
// the model once to be summarised, and from then on the model is sent that summary in
// place of those messages, followed by the recent ones in full. Nothing is deleted: the
// thread keeps every message and the panel shows them all; what changes is what the
// model is sent. A later compaction summarises the earlier summary together with the
// messages since.

const (
	// defaultCompactAt is the size of a thread's last request, in tokens, past which the
	// thread is compacted before the next one.
	defaultCompactAt = 110_000
	// keepRecentChars is about how much of the end of the thread is kept in full (in
	// characters of message content; roughly a third as many tokens).
	keepRecentChars = 90_000
	// summaryInputChars bounds what is handed to the model to summarise.
	summaryInputChars = 400_000
)

const summaryPrompt = `You are writing the running summary of a long conversation between a person and the agent built into an application they are using, so that the agent can go on without the earlier messages. The agent will be sent your summary in place of them, then the most recent messages in full.

Write what the agent needs in order to continue as if it remembered:
- What the person asked for and decided, and any preferences or corrections they gave.
- What was done, with the specifics: what was changed and where, the ids and names involved.
- Facts found along the way that still matter, with the specifics (names, ids, numbers).
- What is unfinished or was promised.

Leave out pleasantries and anything that was superseded. Do not address anyone and do not add advice. Plain text, with short headings if it helps, at most about 1,500 words.`

// historyFor is what the model is sent of a thread's stored messages: the summary of the
// part that has been compacted away, if any, then the messages after it.
func historyFor(msgs []Message, summary string, upto int) []llmtypes.ChatMessage {
	var out []llmtypes.ChatMessage
	for _, m := range msgs {
		if m.Seq <= upto {
			continue
		}
		out = append(out, llmtypes.ChatMessage{Role: m.Role, ContentBlocks: append([]llmtypes.ContentBlock(nil), m.Blocks...)})
	}
	out = repair(out)
	if summary == "" {
		return out
	}
	block := llmtypes.ContentBlock{Type: "text", Text: "<summary>\n" + summary + "\n</summary>"}
	// With the first message kept, when it is the person's: the conversation then still
	// starts with one message from them.
	if len(out) > 0 && out[0].Role == "user" {
		out[0].ContentBlocks = append([]llmtypes.ContentBlock{block}, out[0].ContentBlocks...)
		return out
	}
	return append([]llmtypes.ChatMessage{{Role: "user", ContentBlocks: []llmtypes.ContentBlock{block}}}, out...)
}

func messageChars(m Message) int {
	n := 0
	for _, b := range m.Blocks {
		n += len(b.Text) + len(b.Content)
		if b.Input != nil {
			raw, _ := json.Marshal(b.Input)
			n += len(raw)
		}
	}
	return n
}

// ownerTurn says whether a stored message is one the person typed (the start of a turn),
// as against tool results, which are also stored as the user's.
func ownerTurn(m Message) bool {
	if m.Role != "user" {
		return false
	}
	var meta struct {
		Text string `json:"text"`
	}
	return json.Unmarshal(m.Meta, &meta) == nil && meta.Text != ""
}

// compactCut picks where the kept part of a thread starts: the sequence number of an
// person's message such that everything from it on is about keepChars or less, never
// later than the last message they sent, and 0 when there is nothing before it to summarise.
func compactCut(msgs []Message, upto, keepChars int) int {
	var starts []int // indexes of the person's messages not yet summarised
	for i, m := range msgs {
		if m.Seq > upto && ownerTurn(m) {
			starts = append(starts, i)
		}
	}
	if len(starts) < 2 {
		return 0
	}
	turnSize := func(k int) int {
		end := len(msgs)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		n := 0
		for i := starts[k]; i < end; i++ {
			n += messageChars(msgs[i])
		}
		return n
	}
	// The last turn is always kept; earlier ones while they fit. The first is never kept,
	// so that there is something to summarise.
	cut := starts[len(starts)-1]
	size := turnSize(len(starts) - 1)
	for k := len(starts) - 2; k >= 1; k-- {
		if n := turnSize(k); size+n > keepChars {
			break
		} else {
			size += n
		}
		cut = starts[k]
	}
	return msgs[cut].Seq
}

// transcript writes the part of a thread to be summarised as plain text.
func transcript(msgs []Message, from, before int, earlier string) string {
	var b strings.Builder
	if earlier != "" {
		b.WriteString("Summary of the conversation before this part:\n" + earlier + "\n\n---\n\n")
	}
	for _, m := range msgs {
		if m.Seq <= from || m.Seq >= before {
			continue
		}
		for _, blk := range m.Blocks {
			switch {
			case blk.Type == "text" && m.Role == "user":
				b.WriteString("Person: " + clip(blk.Text, 6000) + "\n\n")
			case blk.Type == "text":
				b.WriteString("Agent: " + clip(blk.Text, 6000) + "\n\n")
			case blk.Type == "tool_use":
				input := ""
				if blk.Input != nil {
					raw, _ := json.Marshal(blk.Input)
					input = clip(string(raw), 800)
				}
				b.WriteString("Agent called " + blk.Name + " " + input + "\n")
			case blk.Type == "tool_result":
				state := "result"
				if blk.IsError {
					state = "failed"
				}
				b.WriteString("  " + state + ": " + clip(blk.Content, 1500) + "\n\n")
			}
		}
	}
	text := b.String()
	if len(text) > summaryInputChars {
		// The end matters most; the start is the part an earlier summary already covers.
		text = "[the start of this part is left out]\n" + text[len(text)-summaryInputChars:]
	}
	return text
}

// compact summarises the older part of the run's thread when its last request was large,
// and sets the run's history to the summary and the recent messages. It reports to the
// page that it is doing so. Any failure leaves the thread as it was.
func (r *run) compact(ctx context.Context) {
	h := r.h
	at := h.cfg.CompactAt
	if at <= 0 {
		return
	}
	if n, _ := h.store.LastPrefix(ctx, r.thread); n < at {
		return
	}
	msgs, err := h.store.Messages(ctx, r.thread)
	if err != nil {
		return
	}
	earlier, upto, err := h.store.Summary(ctx, r.thread)
	if err != nil {
		return
	}
	cut := compactCut(msgs, upto, keepRecentChars)
	if cut == 0 {
		return
	}
	note := func(state string) {
		value, _ := json.Marshal(map[string]string{"state": state})
		r.emit(chatstream.Event{Verb: chatstream.VerbActivity, Kind: "ade.compact", Value: value})
	}
	note("start")
	started := time.Now()
	res, err := turn.ExecuteTurn(ctx, turn.TurnRequest{
		IdleTimeout:     90 * time.Second,
		RequireTerminal: true,
		Stream: func(ctx context.Context) (<-chan llmtypes.StreamEvent, error) {
			return h.provider.StreamChat(ctx, llmtypes.ChatRequest{
				Model: h.cfg.Model, MaxTokens: 3000, SystemPrompt: summaryPrompt,
				Messages: []llmtypes.ChatMessage{{Role: "user", ContentBlocks: []llmtypes.ContentBlock{{Type: "text", Text: transcript(msgs, upto, cut, earlier)}}}},
			})
		},
	})
	if res.Usage != nil {
		r.in += res.Usage.InputTokens + res.Usage.CacheReadTokens + res.Usage.CacheCreationTokens
		r.out += res.Usage.OutputTokens
	}
	summary := strings.TrimSpace(res.Text)
	if err != nil || summary == "" {
		slog.Warn("agenthost: the thread was not compacted", "thread", r.thread, "err", err)
		note("failed")
		return
	}
	if err := h.store.SetSummary(context.WithoutCancel(ctx), r.thread, summary, cut-1); err != nil {
		slog.Warn("agenthost: the summary was not saved", "thread", r.thread, "err", err)
		note("failed")
		return
	}
	r.history = historyFor(msgs, summary, cut-1)
	slog.Info("agenthost: thread compacted", "thread", r.thread, "summarised_to", cut-1, "took", time.Since(started).Round(time.Millisecond).String())
	note("done")
}
