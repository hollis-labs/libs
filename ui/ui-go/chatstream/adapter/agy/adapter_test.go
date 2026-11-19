package agy_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/adapter/agy"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/adapter/claudejson"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/conformance"
)

func TestCapabilities(t *testing.T) {
	a := agy.New()
	c := a.Capabilities()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Name() != "agy.stream-json" || a.Framing() != chatstream.FramingNDJSON || c.Text != chatstream.GranularityToken || c.Approval != chatstream.ApprovalNone || !c.Cancel {
		t.Fatalf("capabilities: %+v", c)
	}
}
func TestFixtures(t *testing.T) { conformance.CheckDecoderDir(t, agy.New(), "testdata") }
func TestTruncationAndCompleteFinalAtEveryFrame(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.frames.json")
	if err != nil || len(paths) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			fx, err := conformance.LoadFixture(p)
			if err != nil {
				t.Fatal(err)
			}
			conformance.CheckTruncation(t, agy.New(), fx)
			for n := 0; n <= len(fx.Frames); n++ {
				prefix := fx
				prefix.Frames = fx.Frames[:n]
				evs, err := conformance.DecodeFixture(agy.New(), prefix)
				if err != nil {
					t.Fatal(err)
				}
				text := map[string]string{}
				args := map[string]string{}
				terminals := 0
				for _, e := range evs {
					if e.IsTerminal() {
						terminals++
					}
					if e.Verb == chatstream.VerbPartDelta {
						if e.Text != "" {
							text[e.PartID] += e.Text
						}
						if e.JSONFragment != "" {
							args[e.PartID] += e.JSONFragment
						}
					}
					if e.Verb == chatstream.VerbPartEnd {
						if !json.Valid(e.Final) {
							t.Fatalf("prefix %d part %s has no valid final: %s", n, e.PartID, e.Final)
						}
						if want, ok := text[e.PartID]; ok {
							var got string
							if json.Unmarshal(e.Final, &got) != nil || got != want {
								t.Fatalf("prefix %d final %s != %q", n, e.Final, want)
							}
						}
						if want, ok := args[e.PartID]; ok && string(e.Final) != want {
							t.Fatalf("arguments final %s != %s", e.Final, want)
						}
					}
				}
				if terminals != 1 {
					t.Fatalf("prefix %d terminals %d", n, terminals)
				}
			}
		})
	}
}
func TestClaudeCannotDecodeRecordedAgy(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/plain.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	old, err := conformance.DecodeFixture(claudejson.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	if old[len(old)-1].Code != chatstream.CodeUpstreamTruncated {
		t.Fatal("reuse premise changed")
	}
	evs, err := conformance.DecodeFixture(agy.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text() != "hello\n" || evs[len(evs)-1].Verb != chatstream.VerbRunFinish {
		t.Fatalf("decoded %+v", m)
	}
}
func TestPermissionDenialIsActivityOnly(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/permission.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := conformance.DecodeFixture(agy.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Verb == chatstream.VerbApprovalRequest {
			t.Fatal("approval bridging")
		}
		if e.Kind == "agy.permission_denied" {
			found = true
		}
	}
	if !found || evs[len(evs)-1].Verb != chatstream.VerbRunFinish {
		t.Fatal("denial lost or treated as failure")
	}
}
func TestRecordedTerminals(t *testing.T) {
	for name, want := range map[string]chatstream.Verb{"cancelled": chatstream.VerbRunAbort, "cancelled_stream": chatstream.VerbRunAbort, "truncated": chatstream.VerbRunError, "failed": chatstream.VerbRunError, "malformed": chatstream.VerbRunFinish} {
		fx, err := conformance.LoadFixture("testdata/" + name + ".frames.json")
		if err != nil {
			t.Fatal(err)
		}
		evs, err := conformance.DecodeFixture(agy.New(), fx)
		if err != nil {
			t.Fatal(err)
		}
		if evs[len(evs)-1].Verb != want {
			t.Fatalf("%s terminal %+v", name, evs[len(evs)-1])
		}
	}
}

func TestResultIsTheOnlyTerminalAndLateFramesAreIgnored(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/plain.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	d := agy.New().NewDecoder(conformance.DecodeOptions())
	for _, f := range fx.Frames[:len(fx.Frames)-1] {
		evs, err := d.Decode(f.Frame)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.IsTerminal() {
				t.Fatal("step ended run")
			}
		}
	}
	term, err := d.Decode(fx.Frames[len(fx.Frames)-1].Frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(term) == 0 || !term[len(term)-1].IsTerminal() {
		t.Fatal("no terminal")
	}
	for _, f := range fx.Frames {
		evs, err := d.Decode(f.Frame)
		if err != nil || len(evs) != 0 {
			t.Fatal("events after terminal", evs, err)
		}
	}
	if evs := d.Close(nil); len(evs) != 0 {
		t.Fatal("close after terminal", evs)
	}
}
func TestRunIDAndResultFallback(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/plain.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"", "override"} {
		d := agy.New().NewDecoder(chatstream.DecodeOptions{RunID: runID})
		var evs []chatstream.Event
		for _, f := range []conformance.TimedFrame{fx.Frames[0], fx.Frames[len(fx.Frames)-1]} {
			got, err := d.Decode(f.Frame)
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, got...)
		}
		evs = append(evs, d.Close(nil)...)
		m, err := chatstream.Reduce(evs, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.Text() != "hello\n" {
			t.Fatal("result-only text lost")
		}
		if runID == "" {
			runID = "b5ee55d5-94d2-4d2f-a4b6-0a53391cfc9d"
		}
		for _, e := range evs {
			if e.RunID != runID {
				t.Fatalf("run id %q != %q", e.RunID, runID)
			}
		}
	}
}
func TestRecordedUsageDoesNotDoubleCountThinking(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/plain.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := conformance.DecodeFixture(agy.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	u := evs[len(evs)-1].Usage
	if u == nil || u.Total() != 9751 || u.Output != 2 || u.Reasoning != 57 {
		t.Fatalf("usage %+v", u)
	}
}
func TestMalformedBytesRemainRaw(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/malformed.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := conformance.DecodeFixture(agy.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Verb == chatstream.VerbRaw && e.Raw.Type == "malformed" {
			var got string
			if json.Unmarshal(e.Raw.Payload, &got) != nil || got != "error: interrupted" {
				t.Fatalf("raw %+v", e.Raw)
			}
			return
		}
	}
	t.Fatal("malformed bytes dropped")
}

func TestRecordedToolsHaveCompleteArgumentsAndResults(t *testing.T) {
	for _, name := range []string{"tool_success", "tool_failure"} {
		t.Run(name, func(t *testing.T) {
			fx, err := conformance.LoadFixture("testdata/" + name + ".frames.json")
			if err != nil {
				t.Fatal(err)
			}
			evs, err := conformance.DecodeFixture(agy.New(), fx)
			if err != nil {
				t.Fatal(err)
			}
			m, err := chatstream.Reduce(evs, nil)
			if err != nil {
				t.Fatal(err)
			}
			calls, results := 0, 0
			for _, p := range m.Parts {
				if p.Kind == chatstream.PartToolCall {
					calls++
					var args map[string]string
					if json.Unmarshal(p.Final, &args) != nil || args["Path"] == "" {
						t.Fatalf("arguments %s", p.Final)
					}
				}
				if p.Kind == chatstream.PartToolResult {
					results++
					var isError bool
					_ = json.Unmarshal(p.Meta[chatstream.MetaIsError], &isError)
					if isError != (name == "tool_failure") {
						t.Fatalf("error metadata %v", p.Meta)
					}
					if name == "tool_success" && string(p.Final) != `"1 lines, 20 bytes"` {
						t.Fatalf("output %s", p.Final)
					}
				}
			}
			want := 1
			if name == "tool_failure" {
				want = 2
			}
			if calls != want || results != want {
				t.Fatalf("calls %d results %d, want %d", calls, results, want)
			}
			if evs[len(evs)-1].Verb != chatstream.VerbRunFinish {
				t.Fatal("tool failure ended run")
			}
			if name == "tool_success" {
				u := evs[len(evs)-1].Usage
				if u == nil || u.UncachedInput != 14241 || u.CacheRead != 9619 || u.Output != 75 || u.Reasoning != 363 || u.Total() != 24298 {
					t.Fatalf("exclusive cache usage %+v", u)
				}
			}
		})
	}
}
