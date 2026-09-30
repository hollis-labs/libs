package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
)

// UpdateEnv is the environment variable that makes CheckDecoder rewrite golden
// files from the decoder's output instead of comparing against them. Review the
// diff before committing it.
const UpdateEnv = "CHATSTREAM_UPDATE_GOLDEN"

// FixedTime is the clock every fixture decode runs on, so golden files carry a
// stable Event.Time.
var FixedTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// FixtureRunID is the run id fixture decodes are given.
const FixtureRunID = "run-1"

// TimedFrame is one recorded upstream frame and how long after the previous one
// it arrived. Delay is for the timing probes (burst versus spread, silent gaps)
// and for replaying against a fake server; decoding ignores it.
type TimedFrame struct {
	Delay time.Duration
	Frame chatstream.Frame
}

// Fixture is a recorded upstream stream.
type Fixture struct {
	Name        string
	Description string
	Frames      []TimedFrame
}

type fixtureFile struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Frames      []fixtureFrame `json:"frames"`
}

type fixtureFrame struct {
	DelayMS  int64           `json:"delay_ms,omitempty"`
	Event    string          `json:"event,omitempty"`
	ID       string          `json:"id,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`      // JSON payload, embedded as is
	DataText string          `json:"data_text,omitempty"` // payload that is not JSON ("[DONE]")
}

// LoadFixture reads a *.frames.json file: {"name", "description", "frames":
// [{"delay_ms", "event", "id", "data" | "data_text"}]}. "data" is a JSON value,
// used byte for byte (compacted); "data_text" is a string for payloads that are
// not JSON, such as "[DONE]".
func LoadFixture(path string) (Fixture, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is the fixture the caller named
	if err != nil {
		return Fixture{}, err
	}
	var ff fixtureFile
	if err := json.Unmarshal(raw, &ff); err != nil {
		return Fixture{}, fmt.Errorf("%s: %w", path, err)
	}
	fx := Fixture{Name: ff.Name, Description: ff.Description}
	if fx.Name == "" {
		fx.Name = strings.TrimSuffix(filepath.Base(path), ".frames.json")
	}
	for i, f := range ff.Frames {
		if len(f.Data) > 0 && f.DataText != "" {
			return Fixture{}, fmt.Errorf("%s: frame %d has both data and data_text", path, i)
		}
		data := []byte(f.DataText)
		if len(f.Data) > 0 {
			var buf bytes.Buffer
			if err := json.Compact(&buf, f.Data); err != nil {
				return Fixture{}, fmt.Errorf("%s: frame %d: %w", path, i, err)
			}
			data = buf.Bytes()
		}
		fx.Frames = append(fx.Frames, TimedFrame{
			Delay: time.Duration(f.DelayMS) * time.Millisecond,
			Frame: chatstream.Frame{Event: f.Event, ID: f.ID, Data: data},
		})
	}
	return fx, nil
}

// DecodeOptions returns the DecodeOptions fixture decodes use.
func DecodeOptions() chatstream.DecodeOptions {
	return chatstream.DecodeOptions{RunID: FixtureRunID, Now: func() time.Time { return FixedTime }}
}

// DecodeFixture feeds fx to a fresh decoder of a, then closes it with a nil
// cause (a clean upstream EOF), and returns every event produced. A stream that
// ended with its terminal frame yields nothing more on Close.
func DecodeFixture(a chatstream.Adapter, fx Fixture) ([]chatstream.Event, error) {
	return decodeFrames(a, fx.Frames, nil)
}

func decodeFrames(a chatstream.Adapter, frames []TimedFrame, cause error) ([]chatstream.Event, error) {
	d := a.NewDecoder(DecodeOptions())
	var out []chatstream.Event
	for i, f := range frames {
		evs, err := d.Decode(f.Frame)
		if err != nil {
			return out, fmt.Errorf("frame %d: %w", i, err)
		}
		out = append(out, evs...)
	}
	return append(out, d.Close(cause)...), nil
}

// CheckDecoder decodes the fixture at path (a *.frames.json file) with a and
// requires: the output validates, the reducer oracle holds, and it equals the
// golden events in the sibling *.golden.json file. With UpdateEnv set it writes
// the golden file instead.
func CheckDecoder(t testing.TB, a chatstream.Adapter, path string) {
	t.Helper()
	fx, err := LoadFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := DecodeFixture(a, fx)
	if err != nil {
		t.Fatalf("decode %s: %v", fx.Name, err)
	}
	Check(t, events)
	if oerr := CheckReplayEquivalence(events); oerr != nil {
		t.Errorf("%s: reducer oracle: %v", fx.Name, oerr)
	}

	golden := strings.TrimSuffix(path, ".frames.json") + ".golden.json"
	got, err := marshalEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(UpdateEnv) != "" {
		if werr := os.WriteFile(golden, got, 0o600); werr != nil {
			t.Fatal(werr)
		}
		t.Logf("wrote %s", golden)
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // the golden beside the fixture the caller named
	if err != nil {
		t.Fatalf("%s: no golden file (%v); run with %s=1 to create it", fx.Name, err, UpdateEnv)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		t.Errorf("%s: decoded events differ from %s\n--- got ---\n%s\n--- want ---\n%s", fx.Name, filepath.Base(golden), got, want)
	}
}

// CheckDecoderDir runs CheckDecoder as a subtest for every *.frames.json in dir.
func CheckDecoderDir(t *testing.T, a chatstream.Adapter, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.frames.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no *.frames.json fixtures in %s", dir)
	}
	for _, p := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(p), ".frames.json"), func(t *testing.T) { CheckDecoder(t, a, p) })
	}
}

func marshalEvents(events []chatstream.Event) ([]byte, error) {
	if events == nil {
		events = []chatstream.Event{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(events); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckTruncation is the failure every decoder must survive. For every prefix of
// fx's frames (none of them, one, two, ...) it feeds the prefix and then ends
// the stream two ways, with a clean EOF and with a broken connection, and
// requires:
//
//   - if the prefix already produced the dialect's own terminal event, ending the
//     stream adds nothing;
//   - otherwise ending it yields exactly one terminal event, a run.error with
//     code chatstream.CodeUpstreamTruncated, retryable, and never a run.finish;
//   - the whole sequence validates: everything open is closed first;
//   - Close is idempotent and Decode after Close returns ErrDecoderClosed.
//
// This is what the audit found swallowed silently in the Anthropic, OpenAI Chat
// and Responses paths: EOF without a terminal event reported as success.
func CheckTruncation(t testing.TB, a chatstream.Adapter, fx Fixture) {
	t.Helper()
	causes := map[string]error{"clean EOF": nil, "broken connection": io.ErrUnexpectedEOF}
	for n := 0; n <= len(fx.Frames); n++ {
		for name, cause := range causes {
			d := a.NewDecoder(DecodeOptions())
			var events []chatstream.Event
			for i, f := range fx.Frames[:n] {
				evs, err := d.Decode(f.Frame)
				if err != nil {
					t.Fatalf("%s: prefix %d: frame %d: %v", fx.Name, n, i, err)
				}
				events = append(events, evs...)
			}
			hadTerminal := hasTerminal(events)
			closing := d.Close(cause)
			label := fmt.Sprintf("%s: after %d of %d frames, %s", fx.Name, n, len(fx.Frames), name)

			if hadTerminal {
				if len(closing) != 0 {
					t.Errorf("%s: the run had ended, but Close produced %d more events (%v)", label, len(closing), verbs(closing))
				}
			} else {
				terms := terminals(closing)
				switch {
				case len(terms) != 1:
					t.Errorf("%s: Close produced %d terminal events, want exactly one (%v)", label, len(terms), verbs(closing))
				case terms[0].Verb != chatstream.VerbRunError || terms[0].Code != chatstream.CodeUpstreamTruncated:
					t.Errorf("%s: terminal is %s %q, want run.error %q (truncation must never look like success)",
						label, terms[0].Verb, terms[0].Code, chatstream.CodeUpstreamTruncated)
				case !terms[0].Retryable:
					t.Errorf("%s: a truncated stream should be retryable", label)
				}
			}
			all := append(events, closing...)
			for _, v := range Validate(all) {
				t.Errorf("%s: %s", label, v)
			}
			if again := d.Close(cause); len(again) != 0 {
				t.Errorf("%s: a second Close produced %d events", label, len(again))
			}
			if _, err := d.Decode(chatstream.Frame{Data: []byte(`{}`)}); !errors.Is(err, chatstream.ErrDecoderClosed) {
				t.Errorf("%s: Decode after Close returned %v, want ErrDecoderClosed", label, err)
			}
		}
	}
}

func hasTerminal(evs []chatstream.Event) bool { return len(terminals(evs)) > 0 }

func terminals(evs []chatstream.Event) []chatstream.Event {
	var out []chatstream.Event
	for _, e := range evs {
		if e.IsTerminal() {
			out = append(out, e)
		}
	}
	return out
}

func verbs(evs []chatstream.Event) []chatstream.Verb {
	out := make([]chatstream.Verb, len(evs))
	for i, e := range evs {
		out[i] = e.Verb
	}
	return out
}
