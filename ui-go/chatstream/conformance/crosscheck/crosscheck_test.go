package crosscheck_test

import (
	"path/filepath"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/acp"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/anthropic"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/claudejson"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/codexjson"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/openaichat"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/openairesponses"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/agui"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/aisdk"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/nanitelegacy"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/native"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/openaicompat"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/sinktest"
)

var adapters = map[string]chatstream.Adapter{
	"anthropic":       anthropic.New(),
	"claudejson":      claudejson.New(),
	"openaichat":      openaichat.New(),
	"openairesponses": openairesponses.New(),
	"acp":             acp.New(),
	"codexjson":       codexjson.New(),
}

var sinks = map[string]func() sink.Encoder{
	"native":       func() sink.Encoder { return native.New() },
	"aisdk":        func() sink.Encoder { return aisdk.New() },
	"agui":         func() sink.Encoder { return agui.New() },
	"openaicompat": func() sink.Encoder { return openaicompat.New() },
	"nanitelegacy": func() sink.Encoder { return nanitelegacy.New() },
}

// toolNames returns the name of every tool_call part in events.
func toolNames(events []chatstream.Event) []string {
	var out []string
	for _, e := range events {
		if e.Verb == chatstream.VerbPartStart && e.PartKind() == chatstream.PartToolCall {
			out = append(out, e.MetaString(chatstream.MetaName))
		}
	}
	return out
}

func TestEveryDialectThroughEverySink(t *testing.T) {
	total := 0
	for aname, a := range adapters {
		paths, err := filepath.Glob(filepath.Join("..", "..", "adapter", aname, "testdata", "*.frames.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("%s: no fixtures (%v)", aname, err)
		}
		for _, p := range paths {
			fx, err := conformance.LoadFixture(p)
			if err != nil {
				t.Fatal(err)
			}
			events, err := conformance.DecodeFixture(a, fx)
			if err != nil {
				t.Fatalf("%s/%s: %v", aname, fx.Name, err)
			}
			names := toolNames(events)
			for sname, mk := range sinks {
				t.Run(aname+"/"+fx.Name+"/"+sname, func(t *testing.T) {
					frames := sinktest.Play(t, mk(), sinktest.Scenario{Name: fx.Name, Events: events})
					if len(frames) == 0 {
						t.Fatal("the encoder wrote nothing")
					}
					out := sinktest.Render(frames)
					// a tool call must reach every target that renders tool calls, under its name
					if sname != "native" {
						for _, name := range names {
							if name != "" && !strings.Contains(out, name) {
								t.Errorf("tool %q does not appear in the %s output:\n%s", name, sname, out)
							}
						}
					}
				})
				total++
			}
		}
	}
	if total < 6*5 {
		t.Fatalf("only %d combinations ran", total)
	}
}

// A Nanite client pairs a tool_result with its tool_call by tool_id, so every
// tool_result frame every dialect's fixtures produce must carry the tool_id of a
// tool_call frame written earlier in the same stream.
func TestNaniteToolResultIDsMatchAnEarlierToolCall(t *testing.T) {
	results := 0
	for aname, a := range adapters {
		paths, err := filepath.Glob(filepath.Join("..", "..", "adapter", aname, "testdata", "*.frames.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("%s: no fixtures (%v)", aname, err)
		}
		for _, p := range paths {
			fx, err := conformance.LoadFixture(p)
			if err != nil {
				t.Fatal(err)
			}
			events, err := conformance.DecodeFixture(a, fx)
			if err != nil {
				t.Fatalf("%s/%s: %v", aname, fx.Name, err)
			}
			frames := sinktest.Play(t, nanitelegacy.New(), sinktest.Scenario{Name: fx.Name, Events: events})
			calls := map[string]bool{}
			for _, f := range frames {
				switch f.Event {
				case "tool_call":
					calls[sinktest.MustJSON(t, f)["tool_id"].(string)] = true
				case "tool_result":
					results++
					id, _ := sinktest.MustJSON(t, f)["tool_id"].(string)
					if !calls[id] {
						t.Errorf("%s/%s: tool_result tool_id %q does not match an earlier tool_call (%v)", aname, fx.Name, id, calls)
					}
				}
			}
		}
	}
	if results == 0 {
		t.Fatal("no fixture produced a tool_result: the check ran on nothing")
	}
}
