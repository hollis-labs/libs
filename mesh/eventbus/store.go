package eventbus

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Loss is the persisted record of protected (high-priority) items the client had
// to drop because its bounded queue or spool was full. It is the last resort
// and is never silent: the record survives a restart, is reported through
// Stats and, when the hub is next reachable, announced by a gap marker event
// built by Config.GapMarker.
type Loss struct {
	Class string    `json:"class"`
	First time.Time `json:"first_dropped_at"`
	Last  time.Time `json:"last_dropped_at"`
	Count uint64    `json:"count"`

	// MarkerID names the gap marker event in flight for the first MarkerCount
	// of these items (up to MarkerLast). The record is reduced only when the
	// hub has accepted that marker, so a lost or rejected marker leaves the
	// loss on record, and losses that happen meanwhile are never erased.
	MarkerID    string    `json:"marker_id,omitempty"`
	MarkerCount uint64    `json:"marker_count,omitempty"`
	MarkerLast  time.Time `json:"marker_last,omitempty"`
}

// lossClass is the Loss.Class of high-priority items.
const lossClass = "protected"

// lossBook holds loss records, in memory and, when path is set, in loss.json.
type lossBook struct {
	path string
	recs map[string]*Loss
}

func openLossBook(dir string) *lossBook {
	b := &lossBook{recs: map[string]*Loss{}}
	if dir == "" {
		return b
	}
	b.path = filepath.Join(dir, "loss.json")
	if data, err := os.ReadFile(b.path); err == nil {
		var list []Loss
		if json.Unmarshal(data, &list) == nil {
			for i := range list {
				l := list[i]
				// A marker queued by an earlier process may not have survived;
				// announce again (a marker that did survive is harmless: it is
				// delivered, and only the marker named here reduces the record).
				l.MarkerID, l.MarkerCount, l.MarkerLast = "", 0, time.Time{}
				b.recs[l.Class] = &l
			}
		}
	}
	return b
}

func (b *lossBook) add(class string, n int, now time.Time) error {
	r := b.recs[class]
	if r == nil {
		r = &Loss{Class: class, First: now}
		b.recs[class] = r
	}
	r.Last = now
	r.Count += uint64(n)
	return b.save()
}

// beginMarker records that a marker with id covers the snapshot's items.
func (b *lossBook) beginMarker(class, id string, snap Loss) error {
	r := b.recs[class]
	if r == nil || r.MarkerID != "" {
		return nil
	}
	r.MarkerID, r.MarkerCount, r.MarkerLast = id, snap.Count, snap.Last
	return b.save()
}

// ackMarker is called when the hub accepted the event id. If it is a marker,
// the items it announced come off the record; losses added since stay.
func (b *lossBook) ackMarker(id string) (bool, error) {
	for class, r := range b.recs {
		if r.MarkerID != id || id == "" {
			continue
		}
		if r.Count <= r.MarkerCount {
			delete(b.recs, class)
		} else {
			r.Count -= r.MarkerCount
			r.First = r.MarkerLast
			r.MarkerID, r.MarkerCount, r.MarkerLast = "", 0, time.Time{}
		}
		return true, b.save()
	}
	return false, nil
}

func (b *lossBook) list() []Loss {
	out := make([]Loss, 0, len(b.recs))
	for _, r := range b.recs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

func (b *lossBook) save() error {
	if b.path == "" {
		return nil
	}
	data, err := json.Marshal(b.list())
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

// quarantine is the bounded dead-letter file for events that were not
// delivered and will not be retried: rejected by the hub, refused as a whole
// request (400, 401, 403, a single event too large), or left without a valid
// verdict too many times. Nothing in it was sent. It lives in the spool
// directory (quarantine.jsonl); oldest records go first when it is over its
// cap. Without a spool directory there is no file and only the callback and the
// counters remain.
type quarantine struct {
	path string
	max  int64
	size int64
}

type quarantineRec struct {
	At      time.Time       `json:"at"`
	Reason  string          `json:"reason"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	ID      string          `json:"id"`
	Prio    Priority        `json:"p"`
	Body    json.RawMessage `json:"b"`
}

func openQuarantine(dir string, max int64) *quarantine {
	q := &quarantine{max: max}
	if dir == "" {
		return q
	}
	q.path = filepath.Join(dir, "quarantine.jsonl")
	if st, err := os.Stat(q.path); err == nil {
		q.size = st.Size()
		// Cut a torn tail so the next append starts on a line boundary.
		if data, err := os.ReadFile(q.path); err == nil {
			if keep := int64(bytes.LastIndexByte(data, '\n') + 1); keep != int64(len(data)) {
				if os.Truncate(q.path, keep) == nil {
					q.size = keep
				}
			}
		}
	}
	return q
}

// addAll appends every record in one write, so a batch is quarantined whole or
// not at all. A failure to append is returned and nothing is recorded; a failure
// to trim afterwards is returned as trimErr, the records themselves being safe.
func (q *quarantine) addAll(recs []quarantineRec) (err, trimErr error) {
	if q.path == "" || len(recs) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	for _, rec := range recs {
		line, err := json.Marshal(rec)
		if err != nil {
			return err, nil
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(q.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err, nil
	}
	n, werr := f.Write(buf.Bytes())
	cerr := f.Close()
	if werr != nil || cerr != nil || n != buf.Len() {
		_ = os.Truncate(q.path, q.size)
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = errors.New("eventbus: short write to quarantine")
		}
		return werr, nil
	}
	q.size += int64(buf.Len())
	if q.max > 0 && q.size > q.max {
		return nil, q.trim()
	}
	return nil, nil
}

// trim drops the oldest records until the file is at most 90% of its cap.
func (q *quarantine) trim() error {
	data, err := os.ReadFile(q.path)
	if err != nil {
		return err
	}
	target := q.max * 9 / 10
	lines := bytes.SplitAfter(data, []byte("\n"))
	total := int64(len(data))
	i := 0
	for i < len(lines) && total > target {
		total -= int64(len(lines[i]))
		i++
	}
	rest := bytes.Join(lines[i:], nil)
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, rest, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, q.path); err != nil {
		return err
	}
	q.size = int64(len(rest))
	return nil
}
