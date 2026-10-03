package ssetest

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	ssekit "github.com/hollis-labs/go-ssekit"
)

// Step is one instruction of a script. See Script.
type Step interface {
	// run executes the step for one connection; it reports whether the
	// connection is over.
	run(c *conn) (end bool)
}

type stepFunc func(c *conn) bool

func (f stepFunc) run(c *conn) bool { return f(c) }

// Request records what a client asked for.
type Request struct {
	Method      string
	LastEventID string
	Query       url.Values
	Header      http.Header
}

// Scripted is the handler returned by Script.
type Scripted struct {
	mu       sync.Mutex
	steps    []Step
	next     int
	cursor   int      // next id to emit
	gaps     [][2]int // ids never to emit
	sent     []int    // ids emitted so far, in order
	requests []Request
}

// Script returns a handler that plays steps in order across successive
// connections: a connection runs steps until one ends it (Drop, Close, Stall, Status,
// or the end of the script), and the next request continues from the next
// step. When the script is exhausted a new request is answered 410 Gone, so a
// misbehaving client cannot loop forever. The event counter starts at id 1.
func Script(steps ...Step) *Scripted {
	return &Scripted{steps: steps, cursor: 1}
}

// Requests returns the requests seen so far, in order.
func (s *Scripted) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

type conn struct {
	s  *Scripted
	w  http.ResponseWriter
	r  *http.Request
	sw *ssekit.Writer
}

// ServeHTTP implements http.Handler.
func (s *Scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method, LastEventID: r.Header.Get("Last-Event-ID"),
		Query: r.URL.Query(), Header: r.Header.Clone(),
	})
	s.mu.Unlock()

	c := &conn{s: s, w: w, r: r}
	for {
		s.mu.Lock()
		if s.next >= len(s.steps) {
			s.mu.Unlock()
			if c.sw == nil {
				http.Error(w, "script exhausted", http.StatusGone)
			}
			return
		}
		st := s.steps[s.next]
		s.next++
		s.mu.Unlock()
		if st.run(c) {
			return
		}
	}
}

// writer opens the event stream on first use.
func (c *conn) writer() *ssekit.Writer {
	if c.sw == nil {
		sw, err := ssekit.NewWriter(c.w)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		c.sw = sw
	}
	return c.sw
}

func (c *conn) send(e ssekit.Event) {
	if err := c.writer().Send(e); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// nextID hands out the next id, skipping gaps. Callers hold no lock.
func (s *Scripted) nextID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for again := true; again; {
		again = false
		for _, g := range s.gaps {
			if s.cursor >= g[0] && s.cursor <= g[1] {
				s.cursor = g[1] + 1
				again = true
			}
		}
	}
	id := s.cursor
	s.cursor++
	s.sent = append(s.sent, id)
	return id
}

func eventFor(id int) ssekit.Event {
	return ssekit.Event{ID: strconv.Itoa(id), Name: "tick", Data: []byte(fmt.Sprintf("payload-%d", id))}
}

// Emit sends n events with the next ids (1, 2, 3, ... skipping Gap ranges and
// continuing after any Overlap rewind). Event i has id "i", name "tick" and
// data "payload-i".
func Emit(n int) Step {
	return stepFunc(func(c *conn) bool {
		for range n {
			c.send(eventFor(c.s.nextID()))
		}
		return false
	})
}

// Burst sends n events back to back in a single write and flush, with no pause,
// to exercise a consumer that reads slower than the server writes.
func Burst(n int) Step {
	return stepFunc(func(c *conn) bool {
		var raw []byte
		for range n {
			e := eventFor(c.s.nextID())
			raw = append(raw, "id: "+e.ID+"\nevent: "+e.Name+"\ndata: "+string(e.Data)+"\n\n"...)
		}
		return c.rawWrite(raw)
	})
}

// Overlap makes the server re-send the last n ids it emitted: the next Emit
// starts n ids earlier, as a server that replays from before the client's
// Last-Event-ID would.
func Overlap(n int) Step {
	return stepFunc(func(c *conn) bool {
		c.s.mu.Lock()
		c.s.cursor = max(1, c.s.cursor-n)
		c.s.mu.Unlock()
		return false
	})
}

// Gap makes the server never emit ids from..to (inclusive): the next Emit that
// would reach them jumps past.
func Gap(from, to int) Step {
	return stepFunc(func(c *conn) bool {
		c.s.mu.Lock()
		c.s.gaps = append(c.s.gaps, [2]int{from, to})
		c.s.mu.Unlock()
		return false
	})
}

// Send writes the given events as they are.
func Send(events ...ssekit.Event) Step {
	return stepFunc(func(c *conn) bool {
		for _, e := range events {
			c.send(e)
		}
		return false
	})
}

// Comment writes a comment frame.
func Comment(text string) Step {
	return stepFunc(func(c *conn) bool {
		if err := c.writer().Comment(text); err != nil {
			panic(http.ErrAbortHandler)
		}
		return false
	})
}

// Retry writes a bare retry: frame.
func Retry(d time.Duration) Step {
	return Send(ssekit.Event{Retry: d})
}

// Raw writes each chunk as its own write and flush, byte for byte: hostile
// framing (CRLF, lone CR, a frame split across chunks) goes here.
func Raw(chunks ...string) Step {
	return stepFunc(func(c *conn) bool {
		for _, ch := range chunks {
			c.rawWrite([]byte(ch))
		}
		return false
	})
}

func (c *conn) rawWrite(b []byte) bool {
	c.writer()
	if _, err := c.w.Write(b); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err := http.NewResponseController(c.w).Flush(); err != nil {
		panic(http.ErrAbortHandler)
	}
	return false
}

// Wait pauses the connection for d (or until the client disconnects).
func Wait(d time.Duration) Step {
	return stepFunc(func(c *conn) bool {
		c.writer()
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-c.r.Context().Done():
			return true
		}
		return false
	})
}

// Stall keeps the connection open and silent for up to d, or until the client
// hangs up, then ends it: the case an idle watchdog exists for. The response
// headers have been sent, so the client is connected but hears nothing.
func Stall(d time.Duration) Step {
	return stepFunc(func(c *conn) bool {
		c.writer()
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-c.r.Context().Done():
		}
		return true
	})
}

// Status answers the request with the given status and no event stream, and
// ends the connection. It must be the first step of its connection.
func Status(code int) Step {
	return stepFunc(func(c *conn) bool {
		if c.sw != nil {
			panic(http.ErrAbortHandler)
		}
		http.Error(c.w, http.StatusText(code), code)
		return true
	})
}

// Drop sends afterN events (as Emit does), then ends the connection abruptly,
// without the chunked terminator, so the client sees an unexpected EOF rather
// than a clean end.
func Drop(afterN int) Step {
	return stepFunc(func(c *conn) bool {
		for range afterN {
			c.send(eventFor(c.s.nextID()))
		}
		c.writer()
		panic(http.ErrAbortHandler)
	})
}

// Close ends the connection cleanly (a well-formed end of the response).
func Close() Step {
	return stepFunc(func(c *conn) bool {
		c.writer()
		return true
	})
}
