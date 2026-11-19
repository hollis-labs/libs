package chat_test

import (
	"encoding/json"
	"testing"

	chat "github.com/hollis-labs/libs/agent/chat"
	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
)

func TestFinalizer(t *testing.T) {
	start := func(id string, kind chatstream.PartKind, meta map[string]json.RawMessage) chatstream.Event {
		return chatstream.Event{Verb: chatstream.VerbPartStart, PartID: id, Kind: string(kind), Meta: meta}
	}
	text := func(id, s string) chatstream.Event {
		return chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: id, Text: s}
	}
	frag := func(id, s string) chatstream.Event {
		return chatstream.Event{Verb: chatstream.VerbPartDelta, PartID: id, JSONFragment: s}
	}
	end := func(id, final string) chatstream.Event {
		return chatstream.Event{Verb: chatstream.VerbPartEnd, PartID: id, Final: json.RawMessage(final)}
	}
	isErr := map[string]json.RawMessage{chatstream.MetaIsError: json.RawMessage("true")}

	for _, tc := range []struct {
		name string
		in   []chatstream.Event
		want string // final on the last event
	}{
		{"text from deltas", []chatstream.Event{start("p", chatstream.PartText, nil), text("p", "Hel"), text("p", "lo"), end("p", "")}, `{"text":"Hello"}`},
		{"refusal from deltas", []chatstream.Event{start("p", chatstream.PartRefusal, nil), text("p", "No."), end("p", "")}, `{"text":"No."}`},
		{"reasoning keeps its signature and gains text",
			[]chatstream.Event{start("p", chatstream.PartReasoning, nil), text("p", "think"), end("p", `{"signature":"abc"}`)},
			`{"signature":"abc","text":"think"}`},
		{"a final with text is left alone",
			[]chatstream.Event{start("p", chatstream.PartText, nil), text("p", "partial"), end("p", `{"text":"the runner's own"}`)},
			`{"text":"the runner's own"}`},
		{"tool_call from fragments",
			[]chatstream.Event{start("p", chatstream.PartToolCall, nil), frag("p", `{"path":`), frag("p", `"a.txt"}`), end("p", "")},
			`{"path":"a.txt"}`},
		{"tool_call final is left alone",
			[]chatstream.Event{start("p", chatstream.PartToolCall, nil), frag("p", `{"a":`), frag("p", `1}`), end("p", `{"a":2}`)}, `{"a":2}`},
		{"tool_call with fragments that are not JSON cannot be filled",
			[]chatstream.Event{start("p", chatstream.PartToolCall, nil), frag("p", `{"a":`), end("p", "")}, ``},
		{"streamed tool_result takes is_error from its start",
			[]chatstream.Event{start("p", chatstream.PartToolResult, isErr), text("p", "boom"), end("p", "")}, `{"is_error":true,"text":"boom"}`},
		{"a whole part cannot be invented", []chatstream.Event{start("p", chatstream.PartText, nil), end("p", "")}, ``},
		{"a whole part keeps its own", []chatstream.Event{start("p", chatstream.PartToolResult, nil), end("p", `{"text":"ok","is_error":false}`)}, `{"text":"ok","is_error":false}`},
		{"parts do not mix",
			[]chatstream.Event{start("a", chatstream.PartText, nil), start("b", chatstream.PartText, nil), text("a", "A"), text("b", "B"), end("b", ""), text("a", "A"), end("a", "")},
			`{"text":"AA"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f chat.Finalizer
			var last chatstream.Event
			for _, ev := range tc.in {
				out := f.Apply(ev)
				if ev.Verb != chatstream.VerbPartEnd && string(mustJSON(t, out)) != string(mustJSON(t, ev)) {
					t.Fatalf("%s was changed", ev.Verb)
				}
				last = out
			}
			if string(last.Final) != tc.want {
				t.Fatalf("final = %s, want %s", last.Final, tc.want)
			}
		})
	}

	t.Run("a terminal event forgets open parts", func(t *testing.T) {
		var f chat.Finalizer
		f.Apply(start("p", chatstream.PartText, nil))
		f.Apply(text("p", "old"))
		f.Apply(chatstream.Event{Verb: chatstream.VerbRunAbort})
		f.Apply(start("p", chatstream.PartText, nil))
		f.Apply(text("p", "new"))
		if got := f.Apply(end("p", "")); string(got.Final) != `{"text":"new"}` {
			t.Fatalf("final = %s", got.Final)
		}
	})

	t.Run("HasFinal", func(t *testing.T) {
		for _, tc := range []struct {
			kind  chatstream.PartKind
			final string
			want  bool
		}{
			{chatstream.PartText, `{"text":""}`, true},
			{chatstream.PartText, `{"signature":"x"}`, false},
			{chatstream.PartText, ``, false},
			{chatstream.PartText, `"just a string"`, false},
			{chatstream.PartToolResult, `{"text":"ok","is_error":false}`, true},
			{chatstream.PartToolCall, `{}`, true},
			{chatstream.PartToolCall, ``, false},
			{chatstream.PartToolCall, `{"a":`, false},
			{chatstream.PartData, ``, true},
		} {
			if got := chat.HasFinal(tc.kind, json.RawMessage(tc.final)); got != tc.want {
				t.Errorf("HasFinal(%s, %q) = %v", tc.kind, tc.final, got)
			}
		}
	})
}
