package ssekit

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"iter"
	"time"
)

// DefaultMaxEventBytes is the default limit on one event block.
const DefaultMaxEventBytes = 1 << 20

// ReadOption configures Read.
type ReadOption func(*readConfig)

type readConfig struct{ maxEvent int }

// WithMaxEventBytes bounds the raw size of one event block (field names, values
// and line breaks, comments included). A larger block ends the stream with
// ErrEventTooLarge. The default is 1 MiB; n <= 0 keeps the default.
func WithMaxEventBytes(n int) ReadOption {
	return func(c *readConfig) {
		if n > 0 {
			c.maxEvent = n
		}
	}
}

// Read parses an event stream following the WHATWG rules: LF, CRLF and lone CR
// line endings, a byte-order mark at the very start, comments, "data:" with or
// without a space, several data lines joined by "\n", ids containing NUL
// ignored, an empty id: resetting the last event id, non-numeric retry:
// ignored, unknown fields ignored, and no dispatch for a block with no data
// line. An event still incomplete when the stream ends is discarded, as the
// specification requires. Payload bytes are not validated as UTF-8.
//
// The sequence yields events; a read error other than a clean EOF is yielded
// once as (Event{}, err) and ends it. Event.ID is the current last event id.
func Read(r io.Reader, o ...ReadOption) iter.Seq2[Event, error] {
	cfg := readConfig{maxEvent: DefaultMaxEventBytes}
	for _, f := range o {
		f(&cfg)
	}
	return func(yield func(Event, error) bool) {
		p := newParser(r, cfg.maxEvent)
		for {
			ev, err := p.next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					yield(Event{}, err)
				}
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// parser is the incremental WHATWG event-stream parser behind Read and the
// client. It never blocks past the end of a line: a CR ends a line at once and
// a directly following LF is skipped when it arrives.
type parser struct {
	br      *bufio.Reader
	max     int
	line    []byte
	size    int
	first   bool // the next completed line is the first of the stream
	skipLF  bool
	data    []byte
	hasData bool
	name    string
	lastID  string
	retry   time.Duration // latest valid retry: value not yet handed out
}

func newParser(r io.Reader, maxEvent int) *parser {
	return &parser{br: bufio.NewReaderSize(r, 4096), max: maxEvent, first: true}
}

// takeRetry returns and clears the latest retry: value seen.
func (p *parser) takeRetry() time.Duration {
	d := p.retry
	p.retry = 0
	return d
}

// next returns the next dispatched event, or io.EOF at the end of the stream
// (discarding any incomplete event), or a read error.
func (p *parser) next() (Event, error) {
	for {
		line, err := p.readLine()
		if err != nil {
			return Event{}, err
		}
		if len(line) == 0 {
			p.size = 0
			if !p.hasData {
				p.name = ""
				p.data = p.data[:0]
				continue
			}
			d := bytes.Clone(p.data[:len(p.data)-1]) // drop the trailing "\n"
			if d == nil {
				d = []byte{}
			}
			ev := Event{ID: p.lastID, Name: p.name, Data: d, Retry: p.takeRetry()}
			p.name, p.hasData = "", false
			p.data = p.data[:0]
			return ev, nil
		}
		if line[0] == ':' {
			continue
		}
		field, value := line, []byte(nil)
		if i := bytes.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], line[i+1:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		}
		switch string(field) {
		case "event":
			p.name = string(value)
		case "data":
			p.data = append(p.data, value...)
			p.data = append(p.data, '\n')
			p.hasData = true
		case "id":
			if !bytes.Contains(value, []byte{0}) {
				p.lastID = string(value)
			}
		case "retry":
			if d, ok := parseRetry(value); ok {
				p.retry = d
			}
		}
	}
}

// maxRetryMillis caps a parsed retry: value so the Duration cannot overflow.
const maxRetryMillis = 1 << 40

func parseRetry(v []byte) (time.Duration, bool) {
	if len(v) == 0 {
		return 0, false
	}
	var ms int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		if ms < maxRetryMillis {
			ms = ms*10 + int64(c-'0')
		}
	}
	return time.Duration(min(ms, maxRetryMillis)) * time.Millisecond, true
}

// readLine returns the next complete line without its terminator. The returned
// slice is only valid until the next call. A partial line at EOF is dropped.
func (p *parser) readLine() ([]byte, error) {
	p.line = p.line[:0]
	for {
		b, err := p.br.ReadByte()
		if err != nil {
			return nil, err
		}
		if p.skipLF {
			p.skipLF = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\n', '\r':
			p.skipLF = b == '\r'
			p.size++
			if p.size > p.max {
				return nil, ErrEventTooLarge
			}
			line := p.line
			if p.first {
				p.first = false
				line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
			}
			return line, nil
		default:
			p.line = append(p.line, b)
			p.size++
			if p.size > p.max {
				return nil, ErrEventTooLarge
			}
		}
	}
}
