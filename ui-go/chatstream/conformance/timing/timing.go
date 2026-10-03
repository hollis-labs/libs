package timing

import (
	"context"
	"iter"
	"slices"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/conformance"
)

// Shape names how a stream's frames were spread over time.
type Shape string

// The shapes.
const (
	// Empty: no frames.
	Empty Shape = "empty"
	// Stalled: some gap between frames (or before the first) reached Options.StallGap,
	// long enough to trip an idle watchdog. It outranks Burst and Spread.
	Stalled Shape = "stalled"
	// Burst: every frame fell inside one Options.Bucket: the stream arrived all
	// at once, so nothing was incremental (a phased or buffered delivery).
	Burst Shape = "burst"
	// Spread: frames arrived over more than one bucket: genuinely incremental.
	Spread Shape = "spread"
)

// Default thresholds; see Options.
const (
	DefaultBurstDelay = 5 * time.Millisecond
	DefaultBucket     = 250 * time.Millisecond
	DefaultStallGap   = 30 * time.Second
)

// Options set the thresholds Profile and Classify use. A zero field takes its default.
type Options struct {
	// BurstDelay: a frame that arrives less than this after the previous one
	// counts toward Report.Burstiness. Default 5ms.
	BurstDelay time.Duration
	// Bucket is the window of the "distinct buckets with content" measure the
	// spike's live-versus-phased probe used: a stream whose frames fall in more
	// than one bucket is incremental. Default 250ms.
	Bucket time.Duration
	// StallGap is the silence that makes a stream Stalled. Default 30s: the cap
	// the spike's cap-test suspected in Nanite's connections, and longer than
	// any keepalive interval in the portfolio (15s).
	StallGap time.Duration
}

func (o Options) withDefaults() Options {
	if o.BurstDelay <= 0 {
		o.BurstDelay = DefaultBurstDelay
	}
	if o.Bucket <= 0 {
		o.Bucket = DefaultBucket
	}
	if o.StallGap <= 0 {
		o.StallGap = DefaultStallGap
	}
	return o
}

// Report summarizes a recorded stream's timing. Delays are the frames'
// TimedFrame.Delay: the wait before each frame, from the previous frame (or
// from the start of the stream for the first).
type Report struct {
	Frames int
	// Total is the time from the start to the last frame.
	Total time.Duration
	// First is the wait before the first frame.
	First time.Duration
	// Min, Median and Max describe the waits BETWEEN frames (frames 2..n); they
	// are zero for fewer than two frames. Median is the lower middle for an even count.
	Min, Median, Max time.Duration
	// LongestGap is the longest silence, counting the wait before the first frame.
	LongestGap time.Duration
	// Burstiness is the fraction of the inter-frame waits shorter than
	// Options.BurstDelay: 1 is every frame back to back, 0 none. Zero for fewer
	// than two frames.
	Burstiness float64
	// Buckets is the number of distinct Options.Bucket windows, counted from the
	// start of the stream, that contain at least one frame.
	Buckets int

	// StallGap is the threshold Classify compares LongestGap with.
	StallGap time.Duration
}

// Profile measures frames.
func Profile(frames []conformance.TimedFrame, opts ...Options) Report {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	o = o.withDefaults()
	r := Report{Frames: len(frames), StallGap: o.StallGap}
	if len(frames) == 0 {
		return r
	}
	r.First = frames[0].Delay
	seen := map[time.Duration]struct{}{}
	var at time.Duration
	var gaps []time.Duration
	burst := 0
	for i, f := range frames {
		at += f.Delay
		seen[at/o.Bucket] = struct{}{}
		r.LongestGap = max(r.LongestGap, f.Delay)
		if i > 0 {
			gaps = append(gaps, f.Delay)
			if f.Delay < o.BurstDelay {
				burst++
			}
		}
	}
	r.Total = at
	r.Buckets = len(seen)
	if len(gaps) > 0 {
		slices.Sort(gaps)
		r.Min, r.Max = gaps[0], gaps[len(gaps)-1]
		r.Median = gaps[(len(gaps)-1)/2]
		r.Burstiness = float64(burst) / float64(len(gaps))
	}
	return r
}

// Classify names the shape of a Report, in this order: Empty for no frames;
// Stalled when LongestGap >= StallGap; Burst when every frame fell in one
// bucket; otherwise Spread.
func Classify(r Report) Shape {
	switch {
	case r.Frames == 0:
		return Empty
	case r.StallGap > 0 && r.LongestGap >= r.StallGap:
		return Stalled
	case r.Buckets <= 1:
		return Burst
	}
	return Spread
}

// Playback yields frames at their recorded offsets: it waits each frame's Delay
// before yielding it, on the clock of the goroutine (fake time inside a
// testing/synctest bubble). If ctx ends while waiting it yields ctx.Err() and
// stops; if the consumer stops, so does the playback. The result is a frame
// source for chatstream.DecodeFrames.
func Playback(ctx context.Context, frames []conformance.TimedFrame) iter.Seq2[chatstream.Frame, error] {
	return func(yield func(chatstream.Frame, error) bool) {
		for _, f := range frames {
			if f.Delay > 0 {
				t := time.NewTimer(f.Delay)
				select {
				case <-t.C:
				case <-ctx.Done():
					t.Stop()
					yield(chatstream.Frame{}, ctx.Err())
					return
				}
			} else if err := ctx.Err(); err != nil {
				yield(chatstream.Frame{}, err)
				return
			}
			if !yield(f.Frame, nil) {
				return
			}
		}
	}
}
