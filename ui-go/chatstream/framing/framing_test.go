package framing_test

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/framing"
)

func collect(t *testing.T, seq func(func(chatstream.Frame, error) bool)) ([]chatstream.Frame, error) {
	t.Helper()
	var out []chatstream.Frame
	var err error
	seq(func(f chatstream.Frame, e error) bool {
		if e != nil {
			err = e
			return false
		}
		out = append(out, f)
		return true
	})
	return out, err
}

func TestSSEFramesEventsWithNamesAndIDs(t *testing.T) {
	in := "event: message_start\ndata: {\"a\":1}\n\n: comment\nid: 7\nevent: delta\ndata: x\ndata: y\n\ndata: [DONE]\n\n"
	frames, err := collect(t, framing.SSE(iotest.OneByteReader(strings.NewReader(in))))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames = %+v", frames)
	}
	if frames[0].Event != "message_start" || string(frames[0].Data) != `{"a":1}` {
		t.Errorf("frame 0 = %+v", frames[0])
	}
	if frames[1].Event != "delta" || string(frames[1].Data) != "x\ny" || frames[1].ID != "7" {
		t.Errorf("frame 1 = %+v (multi-line data joins with a newline)", frames[1])
	}
	if frames[2].Event != "" || string(frames[2].Data) != "[DONE]" {
		t.Errorf("frame 2 = %+v", frames[2])
	}
}

func TestSSEDiscardsAnIncompleteFinalEvent(t *testing.T) {
	frames, err := collect(t, framing.SSE(strings.NewReader("data: whole\n\ndata: half")))
	if err != nil || len(frames) != 1 || string(frames[0].Data) != "whole" {
		t.Fatalf("frames = %+v, err = %v", frames, err)
	}
}

func TestSSEReadErrorEndsTheSequenceWithIt(t *testing.T) {
	boom := errors.New("reset")
	r := io.MultiReader(strings.NewReader("data: a\n\n"), iotest.ErrReader(boom))
	frames, err := collect(t, framing.SSE(r))
	if !errors.Is(err, boom) || len(frames) != 1 {
		t.Fatalf("frames = %+v, err = %v", frames, err)
	}
}

func TestLinesSkipsBlanksTrimsCRLFKeepsBytes(t *testing.T) {
	in := "{\"a\":1}\r\n\r\n  \n{\"b\":2}\n{\"c\":3}"
	frames, err := collect(t, framing.Lines(strings.NewReader(in), 0))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range frames {
		got = append(got, string(f.Data))
	}
	want := `{"a":1} {"b":2} {"c":3}`
	if strings.Join(got, " ") != want {
		t.Fatalf("frames = %q, want %q (the last line has no newline and is still a frame)", got, want)
	}
}

func TestLinesLongLineIsAnErrorNotTruncation(t *testing.T) {
	_, err := collect(t, framing.Lines(strings.NewReader(strings.Repeat("x", 5000)+"\n"), 1024))
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("err = %v", err)
	}
	frames, err := collect(t, framing.Lines(strings.NewReader(strings.Repeat("x", 200_000)+"\n"), 0))
	if err != nil || len(frames) != 1 || len(frames[0].Data) != 200_000 {
		t.Fatalf("a long line under the cap must survive: %d frames, err %v", len(frames), err)
	}
}

func TestLinesFrameDataIsNotAliased(t *testing.T) {
	frames, _ := collect(t, framing.Lines(strings.NewReader("aaa\nbbb\n"), 0))
	frames[0].Data[0] = 'X'
	if string(frames[1].Data) != "bbb" {
		t.Fatal("frames share a buffer")
	}
}
