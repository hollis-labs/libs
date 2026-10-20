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
	"strconv"
	"strings"
)

// spool is a bounded FIFO of items on disk: spool.jsonl plus spool.head, the
// byte offset of the first unread line. It is the overflow area for a Client
// whose hub is slow or down. All methods assume the Client's lock is held.
type spool struct {
	path     string
	headPath string
	max      int64
	size     int64 // bytes in spool.jsonl
	head     int64 // offset of the first unread line
}

type spoolLine struct {
	ID   string          `json:"id"`
	Prio Priority        `json:"p"`
	Body json.RawMessage `json:"b"`
}

func openSpool(dir string, max int64) (*spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("eventbus: create spool dir: %w", err)
	}
	s := &spool{path: filepath.Join(dir, "spool.jsonl"), headPath: filepath.Join(dir, "spool.head"), max: max}
	if st, err := os.Stat(s.path); err == nil {
		s.size = st.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("eventbus: stat spool: %w", err)
	}
	if b, err := os.ReadFile(s.headPath); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && n >= 0 && n <= s.size {
			s.head = n
		}
	}
	return s, nil
}

// pending is the number of unread bytes.
func (s *spool) pending() int64 { return s.size - s.head }

func (s *spool) empty() bool { return s.pending() <= 0 }

func encodeLine(it Item) ([]byte, error) {
	b, err := json.Marshal(spoolLine{ID: it.ID, Prio: it.Priority, Body: it.Body})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// append adds an item at the tail. When the spool is over its cap afterwards,
// the oldest low-priority items go first, then the oldest of any priority; the
// number of items removed is returned.
func (s *spool) append(it Item) (dropped int, err error) {
	line, err := encodeLine(it)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return 0, werr
	}
	if cerr != nil {
		return 0, cerr
	}
	s.size += int64(len(line))
	if s.max > 0 && s.pending() > s.max {
		return s.enforceCap()
	}
	return 0, nil
}

// takeFront removes and returns up to n items from the head.
func (s *spool) takeFront(n int) ([]Item, error) {
	if s.empty() || n <= 0 {
		return nil, nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(s.head, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	var out []Item
	off := s.head
	for len(out) < n {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A torn tail line without a newline is left alone.
			break
		}
		off += int64(len(line))
		var sl spoolLine
		if json.Unmarshal(bytes.TrimSpace(line), &sl) != nil || len(sl.Body) == 0 {
			continue // corrupt line: skip, do not wedge the queue
		}
		out = append(out, Item{ID: sl.ID, Priority: sl.Prio, Body: sl.Body})
	}
	s.head = off
	if s.head >= s.size {
		s.head, s.size = 0, 0
		if err := os.Truncate(s.path, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
	}
	return out, s.writeHead()
}

func (s *spool) writeHead() error {
	return os.WriteFile(s.headPath, []byte(strconv.FormatInt(s.head, 10)), 0o600)
}

// readAll returns every unread item without consuming it.
func (s *spool) readAll() ([]Item, error) {
	if s.empty() {
		return nil, nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(s.head, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	var out []Item
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var sl spoolLine
		if json.Unmarshal(bytes.TrimSpace(line), &sl) != nil || len(sl.Body) == 0 {
			continue
		}
		out = append(out, Item{ID: sl.ID, Priority: sl.Prio, Body: sl.Body})
	}
	return out, nil
}

// rewrite replaces the spool with items (oldest first).
func (s *spool) rewrite(items []Item) error {
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	var size int64
	for _, it := range items {
		line, err := encodeLine(it)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(line); err != nil {
			f.Close()
			return err
		}
		size += int64(len(line))
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.size, s.head = size, 0
	return s.writeHead()
}

// prepend puts items (oldest first) before everything already spooled.
func (s *spool) prepend(items []Item) (dropped int, err error) {
	rest, err := s.readAll()
	if err != nil {
		return 0, err
	}
	if err := s.rewrite(append(append([]Item(nil), items...), rest...)); err != nil {
		return 0, err
	}
	if s.max > 0 && s.pending() > s.max {
		return s.enforceCap()
	}
	return 0, nil
}

// enforceCap drops until the unread bytes fit: oldest PriorityLow first, then
// oldest of any priority.
func (s *spool) enforceCap() (int, error) {
	items, err := s.readAll()
	if err != nil {
		return 0, err
	}
	sizes := make([]int64, len(items))
	var total int64
	for i, it := range items {
		l, _ := encodeLine(it)
		sizes[i] = int64(len(l))
		total += sizes[i]
	}
	keep := make([]bool, len(items))
	for i := range keep {
		keep[i] = true
	}
	dropped := 0
	for pass := 0; pass < 2 && total > s.max; pass++ {
		for i := range items {
			if total <= s.max {
				break
			}
			if keep[i] && (pass == 1 || items[i].Priority == PriorityLow) {
				keep[i] = false
				total -= sizes[i]
				dropped++
			}
		}
	}
	kept := items[:0:0]
	for i, it := range items {
		if keep[i] {
			kept = append(kept, it)
		}
	}
	return dropped, s.rewrite(kept)
}
