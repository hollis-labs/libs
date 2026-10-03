package timing_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/conformance"
	"github.com/hollis-labs/go-chatstream/conformance/timing"
)

const ms = time.Millisecond

func delays(ds ...time.Duration) []conformance.TimedFrame {
	out := make([]conformance.TimedFrame, len(ds))
	for i, d := range ds {
		out[i] = conformance.TimedFrame{Delay: d, Frame: chatstream.Frame{Data: []byte{byte('a' + i%26)}}}
	}
	return out
}

func TestProfile(t *testing.T) {
	tests := []struct {
		name string
		in   []conformance.TimedFrame
		want timing.Report
	}{
		{"empty", nil, timing.Report{StallGap: timing.DefaultStallGap}},
		{"one frame", delays(40 * ms), timing.Report{Frames: 1, Total: 40 * ms, First: 40 * ms, LongestGap: 40 * ms, Buckets: 1, StallGap: timing.DefaultStallGap}},
		{"steady", delays(0, 100*ms, 100*ms, 100*ms), timing.Report{
			Frames: 4, Total: 300 * ms, First: 0, Min: 100 * ms, Median: 100 * ms, Max: 100 * ms, LongestGap: 100 * ms,
			Burstiness: 0, Buckets: 2, StallGap: timing.DefaultStallGap}},
		{"burst then silence", delays(10*ms, 0, 1*ms, 2*ms, 4*ms, 3*time.Second), timing.Report{
			Frames: 6, Total: 3*time.Second + 17*ms, First: 10 * ms, Min: 0, Median: 2 * ms, Max: 3 * time.Second, LongestGap: 3 * time.Second,
			Burstiness: 0.8, Buckets: 2, StallGap: timing.DefaultStallGap}},
		{"even count takes the lower middle", delays(0, 10*ms, 20*ms, 30*ms, 40*ms), timing.Report{
			Frames: 5, Total: 100 * ms, Min: 10 * ms, Median: 20 * ms, Max: 40 * ms, LongestGap: 40 * ms, Burstiness: 0, Buckets: 1, StallGap: timing.DefaultStallGap}},
		{"long wait before the first frame counts as silence", delays(45 * time.Second), timing.Report{
			Frames: 1, Total: 45 * time.Second, First: 45 * time.Second, LongestGap: 45 * time.Second, Buckets: 1, StallGap: timing.DefaultStallGap}},
	}
	for _, tc := range tests {
		got := timing.Profile(tc.in)
		if got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

// A wait of exactly BurstDelay is not back to back; one nanosecond less is.
func TestProfileBurstDelayBoundary(t *testing.T) {
	at := timing.Profile(delays(0, timing.DefaultBurstDelay))
	below := timing.Profile(delays(0, timing.DefaultBurstDelay-1))
	if at.Burstiness != 0 || below.Burstiness != 1 {
		t.Fatalf("at the threshold %v, just below %v", at.Burstiness, below.Burstiness)
	}
}

func TestProfileOptionsOverrideThresholds(t *testing.T) {
	in := delays(0, 50*ms, 50*ms, 50*ms)
	r := timing.Profile(in, timing.Options{BurstDelay: 60 * ms, Bucket: 100 * ms, StallGap: 40 * ms})
	if r.Burstiness != 1 || r.Buckets != 2 || r.StallGap != 40*ms {
		t.Fatalf("%+v", r)
	}
}

// The boundaries of each shape, both sides.
func TestClassify(t *testing.T) {
	bucket := timing.DefaultBucket
	stall := timing.DefaultStallGap
	tests := []struct {
		name string
		in   []conformance.TimedFrame
		want timing.Shape
	}{
		{"no frames", nil, timing.Empty},
		{"one frame arrives at once", delays(0), timing.Burst},
		{"all frames inside one bucket", delays(0, 1*ms, 1*ms, bucket-3*ms), timing.Burst},
		{"the last frame just reaches the next bucket", delays(0, 1*ms, bucket-1*ms), timing.Spread},
		{"steady stream", delays(0, 100*ms, 100*ms, 100*ms, 100*ms), timing.Spread},
		{"one frame late is still one bucket only if it stays inside", delays(bucket - 1*ms), timing.Burst},
		{"silence just under the stall gap is not a stall", delays(0, 100*ms, stall-1*ms), timing.Spread},
		{"silence at the stall gap is a stall", delays(0, 100*ms, stall), timing.Stalled},
		{"a stall before the first frame", delays(stall, 1*ms), timing.Stalled},
		{"a stall outranks a burst", delays(0, 1*ms, stall, 1*ms), timing.Stalled},
	}
	for _, tc := range tests {
		if got := timing.Classify(timing.Profile(tc.in)); got != tc.want {
			t.Errorf("%s: %s, want %s (%+v)", tc.name, got, tc.want, timing.Profile(tc.in))
		}
	}
}

func TestClassifyUsesTheReportsOwnStallGap(t *testing.T) {
	r := timing.Profile(delays(0, 2*time.Second), timing.Options{StallGap: time.Second})
	if got := timing.Classify(r); got != timing.Stalled {
		t.Fatalf("got %s", got)
	}
	if got := timing.Classify(timing.Report{Frames: 2, Buckets: 2, LongestGap: time.Hour}); got != timing.Spread {
		t.Fatalf("a Report with no threshold never stalls: got %s", got)
	}
}

func TestPlaybackHonoursDelaysOnFakeTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		in := delays(10*ms, 0, 90*ms, 400*ms)
		start := time.Now()
		var at []time.Duration
		var data string
		for f, err := range timing.Playback(context.Background(), in) {
			if err != nil {
				t.Fatal(err)
			}
			at = append(at, time.Since(start))
			data += string(f.Data)
		}
		want := []time.Duration{10 * ms, 10 * ms, 100 * ms, 500 * ms}
		if len(at) != len(want) || data != "abcd" {
			t.Fatalf("at %v data %q", at, data)
		}
		for i := range want {
			if at[i] != want[i] {
				t.Errorf("frame %d at %v, want %v", i, at[i], want[i])
			}
		}
	})
}

func TestPlaybackStopsWhenTheContextEndsMidWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*ms)
		defer cancel()
		var got []string
		var err error
		for f, e := range timing.Playback(ctx, delays(10*ms, 10*ms, time.Hour, 10*ms)) {
			if e != nil {
				err = e
				break
			}
			got = append(got, string(f.Data))
		}
		if !errors.Is(err, context.DeadlineExceeded) || len(got) != 2 {
			t.Fatalf("got %v, err %v", got, err)
		}
	})
}

func TestPlaybackWithACancelledContextYieldsNothingEvenWithoutDelays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := 0
	var err error
	for _, e := range timing.Playback(ctx, delays(0, 0)) {
		if e != nil {
			err = e
			break
		}
		n++
	}
	if n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("frames %d, err %v", n, err)
	}
}

func TestPlaybackConsumerCanStopEarly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := 0
		for range timing.Playback(context.Background(), delays(10*ms, 10*ms, 10*ms)) {
			n++
			break
		}
		if n != 1 {
			t.Fatal(n)
		}
	})
}

// Playback is a frame source: a decoder sees the recorded cadence.
func TestPlaybackFeedsDecodeFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		frames := []conformance.TimedFrame{
			{Delay: 20 * ms, Frame: chatstream.Frame{Data: []byte("x")}},
			{Delay: 30 * ms, Frame: chatstream.Frame{Data: []byte("y")}},
		}
		start := time.Now()
		dec := &countDecoder{}
		for range chatstream.DecodeFrames(context.Background(), dec, timing.Playback(context.Background(), frames)) {
		}
		if dec.frames != 2 || !dec.closed || time.Since(start) != 50*ms {
			t.Fatalf("decoder saw %d frames, closed %v, elapsed %v", dec.frames, dec.closed, time.Since(start))
		}
	})
}

type countDecoder struct {
	frames int
	closed bool
}

func (d *countDecoder) Decode(chatstream.Frame) ([]chatstream.Event, error) {
	d.frames++
	return nil, nil
}
func (d *countDecoder) Close(error) []chatstream.Event { d.closed = true; return nil }
