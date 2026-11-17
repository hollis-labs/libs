// Package programstatus parses OSC 7501 reports and binds them to the PTY
// process generation that produced them. Reports are observations, never
// authorization or durable conversation events.
package programstatus

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

const prefix = "\x1b]7501;"

var ErrInvalidReport = errors.New("programstatus: invalid OSC 7501 report")

// State follows revision 0.4 of the Program Status Protocol.
type State string

const (
	Idle    State = "idle"
	Working State = "working"
	Done    State = "done"
	Blocked State = "blocked"
	Error   State = "error"
	Clear   State = "clear"
)

// Report is one valid OSC 7501 observation. ID is empty for the root record.
// Message and Title are decoded single-line UTF-8 text, not terminal markup.
type Report struct {
	State    State
	ID       string
	Kind     string
	App      string
	Title    string
	Message  string
	Progress *uint8
}

// Parse accepts one complete OSC 7501 sequence terminated by ST or BEL.
// Malformed pairs are skipped, as the protocol requires. An invalid state,
// identifier, encoded text, or size discards the whole report.
func Parse(sequence []byte) (Report, error) {
	if len(sequence) > 4096 || !strings.HasPrefix(string(sequence), prefix) {
		return Report{}, ErrInvalidReport
	}
	s := string(sequence)
	switch {
	case strings.HasSuffix(s, "\x1b\\"):
		s = strings.TrimSuffix(s, "\x1b\\")
	case strings.HasSuffix(s, "\a"):
		s = strings.TrimSuffix(s, "\a")
	default:
		return Report{}, ErrInvalidReport
	}
	s = strings.TrimPrefix(s, prefix)
	if s == "?" { // Feature detection is not a status report.
		return Report{}, ErrInvalidReport
	}
	values := make(map[string]string)
	for _, part := range strings.Split(s, ":") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		// A malformed explicit id must not silently redirect a child report
		// onto the root record.
		if key == "id" && !valueChars(value) {
			return Report{}, ErrInvalidReport
		}
		if len(key) == 0 || len(key) > 16 || !lowercase(key) || !valueChars(value) {
			continue
		}
		values[key] = value
	}
	r := Report{State: State(values["state"])}
	switch r.State {
	case Idle, Working, Done, Blocked, Error, Clear:
	default:
		return Report{}, ErrInvalidReport
	}
	r.ID = values["id"]
	_, hasID := values["id"]
	if hasID && !validID(r.ID) {
		return Report{}, ErrInvalidReport
	}
	if r.State == Blocked {
		switch values["kind"] {
		case "permission", "question", "auth":
			r.Kind = values["kind"]
		}
	}
	if r.State == Working || r.State == Blocked {
		if n, ok := progress(values["progress"]); ok {
			r.Progress = &n
		}
	}
	if app := values["app"]; len(app) > 0 && len(app) <= 32 && segmentChars(app) {
		r.App = app
	}
	var err error
	r.Title, err = decodeText(values["title"], 256, 192)
	if err != nil {
		return Report{}, ErrInvalidReport
	}
	r.Message, err = decodeText(values["msg"], 2732, 2048)
	if err != nil {
		return Report{}, ErrInvalidReport
	}
	return r, nil
}

func lowercase(s string) bool {
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func valueChars(s string) bool {
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("_.,+/=-", c)) {
			return false
		}
	}
	return true
}

func segmentChars(s string) bool {
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("_.+-", c)) {
			return false
		}
	}
	return true
}

func validID(id string) bool {
	if len(id) > 128 {
		return false
	}
	parts := strings.Split(id, "/")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 32 || !segmentChars(part) {
			return false
		}
	}
	return true
}

func progress(s string) (uint8, bool) {
	if s == "" || len(s) > 3 {
		return 0, false
	}
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return uint8(n), n <= 100
}

func decodeText(s string, encodedMax, decodedMax int) (string, error) {
	if len(s) > encodedMax {
		return "", ErrInvalidReport
	}
	var b []byte
	var err error
	encoding := base64.RawStdEncoding
	if strings.Contains(s, "=") {
		encoding = base64.StdEncoding
	}
	if b, err = encoding.DecodeString(s); err != nil || len(b) > decodedMax || !utf8.Valid(b) {
		return "", ErrInvalidReport
	}
	for _, c := range string(b) {
		if c <= 0x1f || c == 0x7f || c >= 0x80 && c <= 0x9f {
			return "", ErrInvalidReport
		}
	}
	return string(b), nil
}
