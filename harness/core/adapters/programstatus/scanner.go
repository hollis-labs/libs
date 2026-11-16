package programstatus

import "bytes"

// Observation points to the exact byte offset in a PTY stream where a valid
// report began. RawOffset is not a durable event cursor.
type Observation struct {
	RawOffset int64
	Report    Report
}

// Scanner observes a PTY byte stream without altering it. It accepts reports
// split across reads and discards oversized/malformed sequences.
type Scanner struct {
	pending    []byte
	baseOffset int64
	nextOffset int64
	started    bool
}

// Feed requires contiguous raw offsets. A gap resets partial parsing; the
// caller still owns PTY replay and any durable event append independently.
func (s *Scanner) Feed(rawOffset int64, data []byte) []Observation {
	if rawOffset < 0 || len(data) == 0 {
		return nil
	}
	if !s.started || rawOffset != s.nextOffset {
		s.pending = nil
		s.baseOffset = rawOffset
		s.started = true
	}
	s.nextOffset = rawOffset + int64(len(data))
	s.pending = append(s.pending, data...)
	var out []Observation
	for len(s.pending) > 0 {
		start := bytes.Index(s.pending, []byte(prefix))
		if start < 0 {
			// Keep enough of the tail to detect a split prefix in the next read.
			keep := min(len(s.pending), len(prefix)-1)
			s.drop(len(s.pending) - keep)
			break
		}
		s.drop(start)
		end, terminator := sequenceEnd(s.pending)
		if end < 0 {
			if len(s.pending) > 4096 {
				s.drop(1)
				continue
			}
			break
		}
		sequence := s.pending[:end+terminator]
		if r, err := Parse(sequence); err == nil {
			out = append(out, Observation{RawOffset: s.baseOffset, Report: r})
		}
		s.drop(len(sequence))
	}
	return out
}

func (s *Scanner) drop(n int) {
	s.pending = s.pending[n:]
	s.baseOffset += int64(n)
}

func sequenceEnd(b []byte) (int, int) {
	for i := len(prefix); i < len(b); i++ {
		if b[i] == '\a' {
			return i, 1
		}
		if b[i] == '\x1b' && i+1 < len(b) && b[i+1] == '\\' {
			return i, 2
		}
	}
	return -1, 0
}
