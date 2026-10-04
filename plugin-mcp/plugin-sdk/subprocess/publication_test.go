package subprocess

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type writerRecipe struct {
	Directions []string `json:"directions"`
	Fairness   struct {
		Burst    int `json:"control_burst"`
		Ordinary int `json:"ordinary_frames"`
		Controls int `json:"control_seed_frames"`
	} `json:"fairness"`
	Frames struct {
		Frames int `json:"frames"`
		Bytes  int `json:"bytes"`
		Size   int `json:"frame_bytes"`
	} `json:"frame_saturation"`
	Bytes struct {
		Frames int `json:"frames"`
		Bytes  int `json:"bytes"`
		Size   int `json:"frame_bytes"`
		Active int `json:"active_frame_bytes"`
	} `json:"byte_saturation"`
	Terminal struct {
		Forward int `json:"forward"`
		Reverse int `json:"reverse"`
		Control int `json:"control"`
		Credit  int `json:"credit_bytes"`
		Frames  int `json:"lane_frames"`
		Bytes   int `json:"lane_bytes"`
	} `json:"terminal_reservation"`
}

func loadWriterRecipe(t *testing.T) writerRecipe {
	t.Helper()
	b, err := os.ReadFile("../protocol/v2/fixtures/duplex-saturation.json")
	if err != nil {
		t.Fatal(err)
	}
	var r writerRecipe
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

type gatedWriter struct {
	started chan string
	release chan struct{}
}

func (w *gatedWriter) Write(frame []byte) (int, error) {
	w.started <- string(frame)
	<-w.release
	return len(frame), nil
}
func writerRig(t *testing.T, limits QueueLimits) (*frameWriter, *gatedWriter) {
	t.Helper()
	out := &gatedWriter{make(chan string, 128), make(chan struct{}, 128)}
	w := newFrameWriterWithLimits(out, time.Second, func() { out.release <- struct{}{} }, limits)
	t.Cleanup(func() {
		w.abort(errConnectionClosed)
		select {
		case <-w.done:
		case <-time.After(time.Second):
			t.Error("writer did not close")
		}
	})
	return w, out
}
func startedFrame(t *testing.T, out *gatedWriter) string {
	t.Helper()
	select {
	case frame := <-out.started:
		return frame
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
		return ""
	}
}
func TestWriterLaneSaturation(t *testing.T) {
	r := loadWriterRecipe(t)
	for _, tc := range []struct {
		name                        string
		frames, bytes, size, active int
	}{
		{"frames", r.Frames.Frames, r.Frames.Bytes, r.Frames.Size, 8},
		{"bytes", r.Bytes.Frames, r.Bytes.Bytes, r.Bytes.Size, r.Bytes.Active},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, out := writerRig(t, QueueLimits{tc.frames, tc.bytes})
			if err := w.submit([]byte(strings.Repeat("h", tc.active)), nil); err != nil {
				t.Fatal(err)
			}
			startedFrame(t, out)
			count := min(tc.frames, tc.bytes/tc.size)
			for i := 0; i < count; i++ {
				if err := w.submit([]byte(strings.Repeat("o", tc.size)), nil); err != nil {
					t.Fatal(err)
				}
				if err := w.submitControl([]byte(strings.Repeat("c", tc.size)), nil); err != nil {
					t.Fatal(err)
				}
			}
			if !errors.Is(w.submit([]byte(strings.Repeat("o", tc.size)), nil), errPublicationFull) || !errors.Is(w.submitControl([]byte(strings.Repeat("c", tc.size)), nil), errPublicationFull) {
				t.Fatal("lane accepted beyond cap")
			}
			w.mu.Lock()
			queued := w.ordinary.bytes + w.control.bytes
			active := w.activeBytes
			w.mu.Unlock()
			if queued != 2*count*tc.size || active != tc.active {
				t.Fatalf("queued=%d active=%d", queued, active)
			}
		})
	}
}
func TestWriterFairnessBothDirections(t *testing.T) {
	r := loadWriterRecipe(t)
	for _, direction := range r.Directions {
		t.Run(direction, func(t *testing.T) {
			w, out := writerRig(t, QueueLimits{DefaultQueueFrames, DefaultQueuedWriteBytes})
			if err := w.submit([]byte("hold\n"), nil); err != nil {
				t.Fatal(err)
			}
			startedFrame(t, out)
			for i := 0; i < r.Fairness.Ordinary; i++ {
				if err := w.submit([]byte("ordinary\n"), nil); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < r.Fairness.Controls; i++ {
				if err := w.submitControl([]byte("control\n"), nil); err != nil {
					t.Fatal(err)
				}
			}
			seen, burst := 0, 0
			for seen < r.Fairness.Ordinary {
				out.release <- struct{}{}
				frame := startedFrame(t, out)
				if frame == "ordinary\n" {
					if burst > r.Fairness.Burst {
						t.Fatalf("ordinary starved after %d control frames", burst)
					}
					seen++
					burst = 0
				} else {
					burst++
					if burst > r.Fairness.Burst {
						t.Fatal("control burst exceeded")
					}
					if err := w.submitControl([]byte("control\n"), nil); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
func TestWriterTerminalCredit(t *testing.T) {
	r := loadWriterRecipe(t)
	v := r.Terminal
	w, out := writerRig(t, QueueLimits{v.Frames, v.Bytes})
	w.submit([]byte("hold\n"), nil)
	startedFrame(t, out)
	count := v.Forward + v.Reverse + v.Control
	credits := make([]*terminalCredit, 0, count)
	for i := 0; i < count; i++ {
		credit, err := w.reserveTerminal(v.Credit)
		if err != nil {
			t.Fatal(err)
		}
		credits = append(credits, credit)
	}
	w.mu.Lock()
	frames, bytes := w.control.reservedFrames, w.control.reservedBytes
	w.mu.Unlock()
	if frames != count || bytes != count*v.Credit || frames >= v.Frames {
		t.Fatalf("reserved frames=%d bytes=%d", frames, bytes)
	}
	// A large result cannot steal another admitted request's terminal capacity.
	large := make([]byte, v.Bytes-(count-1)*v.Credit+1)
	if !errors.Is(w.submitTerminal(large, credits[0], nil), errPublicationFull) {
		t.Fatal("large result stole reserved capacity")
	}
	committed := []byte(`{"contract":"host-rpc/1","code":"budget_exceeded","request_id":1,"effect_state":"committed","retryable":false}` + "\n")
	if err := w.submitTerminal(committed, credits[0], nil); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	json.Unmarshal(committed, &fields)
	out.release <- struct{}{}
	emitted := startedFrame(t, out)
	if emitted != string(committed) {
		t.Fatalf("fallback bytes changed: %s", emitted)
	}
	if fields["effect_state"] != "committed" || len(committed) > v.Credit {
		t.Fatal("committed fallback lost classification")
	}
	if !errors.Is(w.submitTerminal(committed, credits[0], nil), errPublicationFull) {
		t.Fatal("credit reused")
	}
	for _, credit := range credits[1:] {
		credit.release()
		credit.release()
	}
	w.mu.Lock()
	frames, bytes = w.control.reservedFrames, w.control.reservedBytes
	w.mu.Unlock()
	if frames != 0 || bytes != 0 {
		t.Fatal("credits leaked")
	}
}
func TestWriterReceiptsSettleOnceOnAbort(t *testing.T) {
	w, out := writerRig(t, QueueLimits{32, 1024})
	done := make(chan error, 4)
	receipt := func(err error) { done <- err }
	w.submit([]byte("active\n"), receipt)
	startedFrame(t, out)
	w.submit([]byte("ordinary\n"), receipt)
	w.submitControl([]byte("control\n"), receipt)
	failure := errors.New("fenced")
	w.abort(failure)
	w.abort(failure)
	<-w.done
	if len(done) != 3 {
		t.Fatalf("receipts=%d", len(done))
	}
	for i := 0; i < 3; i++ {
		if err := <-done; !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
}
