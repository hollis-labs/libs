package eventbus

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// spool is a bounded FIFO of items on disk. Records are JSON lines in
// spool-<gen>.jsonl; spool.state holds "<gen> <head>", where head is the byte
// offset of the first record the hub has not yet settled.
//
// There are three offsets into the live file:
//
//	head  first unacknowledged record (durable, in spool.state)
//	pos   next record to load into memory (head <= pos <= size)
//	size  end of file
//
// Records between head and pos are in flight: loaded into the Client's memory
// queue but still on disk until the hub settles them (peek, then ack), so a
// crash during a request loses nothing.
//
// The file is replaced whole (compaction, cap enforcement, prepend) by writing
// spool-<gen+1>.jsonl, syncing it, switching spool.state to the new generation
// with an atomic rename, then removing the old file. A crash at any point
// leaves either the old pair or the new pair valid; stray files of other
// generations are removed when the spool is opened. A torn last line (a crash
// mid-write) is cut off when the spool is opened and after a failed append.
//
// Appends are not synced one by one, so the spool survives a process crash but
// not necessarily a power loss. All methods assume the Client's lock is held.
type spool struct {
	dir  string
	max  int64 // cap on unread bytes (size - head)
	gen  uint64
	size int64
	head int64
	pos  int64
}

type spoolLine struct {
	ID   string          `json:"id"`
	Prio Priority        `json:"p"`
	Body json.RawMessage `json:"b"`
}

// queued is an item in the Client's memory queue. end is the offset in the
// spool just after the item's record, or 0 for an item that never touched the
// spool.
type queued struct {
	Item
	end int64
}

func encodeLine(it Item) ([]byte, error) {
	b, err := json.Marshal(spoolLine{ID: it.ID, Prio: it.Priority, Body: it.Body})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (s *spool) filePath(gen uint64) string {
	return filepath.Join(s.dir, fmt.Sprintf("spool-%d.jsonl", gen))
}

func (s *spool) statePath() string { return filepath.Join(s.dir, "spool.state") }

func openSpool(dir string, max int64) (*spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("eventbus: create spool dir: %w", err)
	}
	s := &spool{dir: dir, max: max, gen: 1}
	if b, err := os.ReadFile(s.statePath()); err == nil {
		var g uint64
		var h int64
		if n, _ := fmt.Sscanf(strings.TrimSpace(string(b)), "%d %d", &g, &h); n == 2 && g > 0 && h >= 0 {
			s.gen, s.head = g, h
		}
	}
	// Remove files of any other generation (leftovers of an interrupted swap).
	if matches, err := filepath.Glob(filepath.Join(dir, "spool-*.jsonl*")); err == nil {
		keep := s.filePath(s.gen)
		for _, m := range matches {
			if m != keep {
				_ = os.Remove(m)
			}
		}
	}
	data, err := os.ReadFile(s.filePath(s.gen))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("eventbus: read spool: %w", err)
	}
	// Cut a torn tail so later appends start on a line boundary.
	keep := int64(bytes.LastIndexByte(data, '\n') + 1)
	if keep != int64(len(data)) {
		if err := os.Truncate(s.filePath(s.gen), keep); err != nil {
			return nil, fmt.Errorf("eventbus: repair spool tail: %w", err)
		}
	}
	s.size = keep
	if s.head > s.size {
		s.head = s.size
	}
	s.pos = s.head
	if err := s.writeState(s.gen, s.head); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *spool) unread() int64 { return s.size - s.head }

func (s *spool) hasUnloaded() bool { return s.size > s.pos }

func (s *spool) writeState(gen uint64, head int64) error {
	tmp := s.statePath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d %d\n", gen, head); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// swap installs content as generation gen+1 with the given head, then removes
// the previous file. offsets other than size/head/pos are the caller's to set.
func (s *spool) swap(content []byte, head int64) error {
	next := s.gen + 1
	path := s.filePath(next)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := s.writeState(next, head); err != nil {
		_ = os.Remove(path)
		return err
	}
	_ = os.Remove(s.filePath(s.gen))
	s.gen = next
	s.size = int64(len(content))
	return nil
}

func (s *spool) readFrom(off int64) ([]byte, error) {
	f, err := os.Open(s.filePath(s.gen))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && off == 0 && s.size == 0 {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, s.size-off))
}

// lossCounts is how many items of each priority a cap forced out, indexed by
// Priority.
type lossCounts [3]int

func (l lossCounts) total() int { return l[0] + l[1] + l[2] }

// append adds an item at the tail and returns how many items the cap forced
// out (the new item included, if it was one of them), by priority.
func (s *spool) append(it Item) (lost lossCounts, err error) {
	line, err := encodeLine(it)
	if err != nil {
		return lost, err
	}
	f, err := os.OpenFile(s.filePath(s.gen), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return lost, err
	}
	n, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil || cerr != nil || n != len(line) {
		// Cut whatever part of the line reached the disk so the next append
		// does not glue itself onto a fragment.
		_ = os.Truncate(s.filePath(s.gen), s.size)
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = io.ErrShortWrite
		}
		return lost, werr
	}
	s.size += int64(len(line))
	if s.max > 0 && s.unread() > s.max {
		return s.enforceCap()
	}
	return lost, nil
}

// enforceCap drops records beyond pos (never an in-flight one) until the unread
// bytes are at most 90% of the cap: the oldest debug records first, then the
// oldest activity records, and protected records only as the last resort. The prefix before pos is kept byte for byte, so the offsets of
// in-flight items stay valid.
func (s *spool) enforceCap() (lost lossCounts, err error) {
	region, err := s.readFrom(s.pos)
	if err != nil {
		return lost, err
	}
	prefix, err := s.readRange(0, s.pos)
	if err != nil {
		return lost, err
	}
	budget := s.max*9/10 - (s.pos - s.head)
	if budget < 0 {
		budget = 0
	}
	type rec struct {
		line []byte
		prio Priority
		keep bool
	}
	var recs []rec
	var total int64
	for _, line := range bytes.SplitAfter(region, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var sl spoolLine
		_ = json.Unmarshal(bytes.TrimSpace(line), &sl)
		recs = append(recs, rec{line: line, prio: sl.Prio, keep: true})
		total += int64(len(line))
	}
	for pass := PriorityDebug; pass <= PriorityHigh && total > budget; pass++ {
		for i := range recs {
			if total <= budget {
				break
			}
			if recs[i].keep && recs[i].prio == pass {
				recs[i].keep = false
				total -= int64(len(recs[i].line))
				lost[pass]++
			}
		}
	}
	var out bytes.Buffer
	out.Write(prefix)
	for _, r := range recs {
		if r.keep {
			out.Write(r.line)
		}
	}
	if err := s.swap(out.Bytes(), s.head); err != nil {
		return lossCounts{}, err
	}
	return lost, nil
}

func (s *spool) readRange(from, to int64) ([]byte, error) {
	if to <= from {
		return nil, nil
	}
	f, err := os.Open(s.filePath(s.gen))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, to-from))
}

// peek loads up to n records from pos and advances pos. They stay on disk until
// ack. A run of unreadable lines at the end is acknowledged on the spot so it
// cannot sit in front of the queue forever.
func (s *spool) peek(n int) ([]queued, error) {
	if n <= 0 || !s.hasUnloaded() {
		return nil, nil
	}
	f, err := os.Open(s.filePath(s.gen))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(s.pos, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(io.LimitReader(f, s.size-s.pos), 64<<10)
	var out []queued
	off := s.pos
	for len(out) < n {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		off += int64(len(line))
		var sl spoolLine
		if json.Unmarshal(bytes.TrimSpace(line), &sl) != nil || sl.ID == "" || len(sl.Body) == 0 {
			continue
		}
		out = append(out, queued{Item: Item{ID: sl.ID, Priority: sl.Prio, Body: sl.Body}, end: off})
	}
	s.pos = off
	if len(out) == 0 && off > s.head {
		_, err := s.ackTo(off)
		return nil, err
	}
	return out, nil
}

// ackTo records that everything before off is settled. It returns the amount
// the offsets of the remaining records moved down by (compaction), which the
// caller applies to the items it still holds.
func (s *spool) ackTo(off int64) (shift int64, err error) {
	if off > s.size {
		off = s.size
	}
	if off <= s.head {
		return 0, nil
	}
	s.head = off
	if s.pos < s.head {
		s.pos = s.head
	}
	if s.head == s.size {
		// Everything is settled: start over with an empty file.
		if err := os.Truncate(s.filePath(s.gen), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		shift = s.head
		s.size, s.head, s.pos = 0, 0, 0
		return shift, s.writeState(s.gen, 0)
	}
	if s.head*4 >= s.size {
		// A quarter or more of the file is dead weight: drop it.
		tail, err := s.readFrom(s.head)
		if err != nil {
			return 0, err
		}
		shift = s.head
		pos := s.pos - shift
		if err := s.swap(tail, 0); err != nil {
			return 0, err
		}
		s.head, s.pos = 0, pos
		return shift, nil
	}
	return 0, s.writeState(s.gen, s.head)
}

// prepend puts items (oldest first) in front of every unsettled record.
func (s *spool) prepend(items []Item) (lost lossCounts, err error) {
	rest, err := s.readFrom(s.head)
	if err != nil {
		return lost, err
	}
	var buf bytes.Buffer
	for _, it := range items {
		line, err := encodeLine(it)
		if err != nil {
			return lost, err
		}
		buf.Write(line)
	}
	buf.Write(rest)
	if err := s.swap(buf.Bytes(), 0); err != nil {
		return lost, err
	}
	s.head, s.pos = 0, 0
	if s.max > 0 && s.unread() > s.max {
		return s.enforceCap()
	}
	return lost, nil
}
