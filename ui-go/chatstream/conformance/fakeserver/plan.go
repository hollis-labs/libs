package fakeserver

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	ssekit "github.com/hollis-labs/go-ssekit"
	"github.com/hollis-labs/go-ssekit/ssetest"
)

// DefaultStall is how long Stall (and the Stuck scenario) keeps a connection
// silent. It ends earlier when the client hangs up; in a synctest bubble it is
// fake time.
const DefaultStall = time.Hour

// Plan is a script over a fixed list of frames. Each Send serves a slice of the
// frames on the current connection; the calls that end a connection (Drop,
// Close, Stall, Status) start the next one. A Plan is a description: Serve and
// Memory each build a fresh, independent script from it.
type Plan struct {
	frames []chatstream.Frame
	steps  []ssetest.Step
}

// NewPlan starts a plan over frames. A frame served without an ID gets its
// position as its id: "1" for frames[0], "2" for frames[1], and so on, so a
// reconnect can be reasoned about by index.
func NewPlan(frames []chatstream.Frame) *Plan {
	return &Plan{frames: append([]chatstream.Frame(nil), frames...)}
}

func (p *Plan) add(s ssetest.Step) *Plan { p.steps = append(p.steps, s); return p }

func (p *Plan) event(i int) ssekit.Event {
	f := p.frames[i]
	id := f.ID
	if id == "" {
		id = strconv.Itoa(i + 1)
	}
	return ssekit.Event{ID: id, Name: f.Event, Data: f.Data}
}

// Send serves frames[from:to] on the current connection. It panics on a range
// outside the frames, since that is a mistake in the test.
func (p *Plan) Send(from, to int) *Plan {
	if from < 0 || to < from || to > len(p.frames) {
		panic("fakeserver: Send range outside the plan's frames")
	}
	evs := make([]ssekit.Event, 0, to-from)
	for i := from; i < to; i++ {
		evs = append(evs, p.event(i))
	}
	return p.add(ssetest.Send(evs...))
}

// Close ends the current connection cleanly, as a server that finished.
func (p *Plan) Close() *Plan { return p.add(ssetest.Close()) }

// Drop ends the current connection abruptly, without the end of the response,
// so the client sees an unexpected EOF rather than a clean end.
func (p *Plan) Drop() *Plan { return p.add(ssetest.Drop(0)) }

// Stall keeps the current connection open and silent for d (DefaultStall when
// d <= 0), or until the client hangs up.
func (p *Plan) Stall(d time.Duration) *Plan {
	if d <= 0 {
		d = DefaultStall
	}
	return p.add(ssetest.Stall(d))
}

// Status answers the next request with the given status and no event stream. It
// must start a connection: put it after a Drop, Close or Stall (or first).
func (p *Plan) Status(code int) *Plan { return p.add(ssetest.Status(code)) }

// NotFound answers the next request 404, which go-ssekit's client treats as final.
func (p *Plan) NotFound() *Plan { return p.Status(http.StatusNotFound) }

// Comment writes an SSE comment (a keepalive) on the current connection.
func (p *Plan) Comment(text string) *Plan { return p.add(ssetest.Comment(text)) }

// Complete serves every frame on one connection and ends it cleanly.
func Complete(frames []chatstream.Frame) *Plan {
	p := NewPlan(frames)
	return p.Send(0, len(frames)).Close()
}

// DropAfter serves the first n frames and cuts the connection. A client that
// reconnects finds the script exhausted (410).
func DropAfter(frames []chatstream.Frame, n int) *Plan {
	return NewPlan(frames).Send(0, n).Drop()
}

// Overlap serves frames[:cut] and cuts the connection; on the reconnect it
// replays from replay frames earlier than the cut (frames[cut-replay:]) and
// closes: a server that replays from before the client's Last-Event-ID.
func Overlap(frames []chatstream.Frame, cut, replay int) *Plan {
	return NewPlan(frames).Send(0, cut).Drop().Send(max(0, cut-replay), len(frames)).Close()
}

// Gap serves frames[:cut] and cuts the connection; on the reconnect it skips
// skip frames (frames[cut+skip:]) and closes: events the client never sees.
func Gap(frames []chatstream.Frame, cut, skip int) *Plan {
	return NewPlan(frames).Send(0, cut).Drop().Send(min(len(frames), cut+skip), len(frames)).Close()
}

// ResumeNotFound serves frames[:cut], cuts the connection, and answers the
// resume 404: the run is gone, and a client must not retry.
func ResumeNotFound(frames []chatstream.Frame, cut int) *Plan {
	return NewPlan(frames).Send(0, cut).Drop().NotFound()
}

// Stuck serves the first n frames and then goes silent with the connection open:
// the case an idle watchdog exists for.
func Stuck(frames []chatstream.Frame, n int) *Plan {
	return NewPlan(frames).Send(0, n).Stall(0)
}

func (p *Plan) script() *ssetest.Scripted {
	return ssetest.Script(append([]ssetest.Step(nil), p.steps...)...)
}

// Upstream is a running fake server. URL and Client are what a test points its
// code at; Client already knows how to reach URL.
type Upstream struct {
	URL    string
	Client *http.Client
	script *ssetest.Scripted
}

// Requests returns every request received, in order.
func (u *Upstream) Requests() []ssetest.Request { return u.script.Requests() }

// LastEventIDs returns the Last-Event-ID header of each request, in order: what
// the client asked to resume from on each attempt ("" for a first connection).
func (u *Upstream) LastEventIDs() []string {
	reqs := u.script.Requests()
	ids := make([]string, len(reqs))
	for i, r := range reqs {
		ids[i] = r.LastEventID
	}
	return ids
}

// Serve starts a real httptest server for p, closed when t finishes.
func (p *Plan) Serve(t testing.TB) *Upstream {
	t.Helper()
	s := p.script()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &Upstream{URL: srv.URL, Client: srv.Client(), script: s}
}

// Memory serves p without sockets: the returned Client calls the script
// in-process through pipes. Use it inside testing/synctest, where a socket read
// would keep fake time from advancing. URL is a placeholder host that only this
// Client resolves.
func (p *Plan) Memory() *Upstream {
	s := p.script()
	return &Upstream{URL: "http://fakeserver.invalid/stream", Client: &http.Client{Transport: memTransport{h: s}}, script: s}
}
