package shim

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/hollis-labs/substrate/mesh"
)

const segmentLimit = 1 << 20
const terminalReserve = 16 << 10

var segmentHeader = []byte("SHIMLOG1\n")

type journalIdentity struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Generation uint64 `json:"generation"`
}

// Journal serializes append, fsync, replay snapshots and subscriptions under
// one lock. Retention never evicts unacknowledged evidence. v0 retains all
// records to preserve idempotency receipts, then fails closed at its disk cap.
// The resident index is bounded by that same cap. Close only after producers stop.
type Journal struct {
	mu          sync.Mutex
	identity    journalIdentity
	dir         string
	file        *os.File
	lock        *os.File
	size        int64
	segmentSize int64
	ordinal     int
	cap         int64
	events      []mesh.Event
	subscribers map[*subscription]struct{}
	failure     error
	syncFile    func(*os.File) error
}

type subscription struct {
	queue chan mesh.Event
	done  chan struct{}
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fault("unsafe_path", "directory must be a private non-symlink directory")
	}
	return nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func OpenJournal(dir, session string, generation uint64, capacity int64) (*Journal, error) {
	if session == "" || generation == 0 || capacity < 2*segmentLimit {
		return nil, fault("invalid_request", "invalid journal identity or capacity")
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	l, err := openLock(filepath.Join(dir, "owner.lock"), true)
	if err != nil {
		return nil, err
	}
	j := &Journal{dir: dir, lock: l, cap: capacity, subscribers: make(map[*subscription]struct{}), syncFile: func(f *os.File) error { return f.Sync() }}
	ok := false
	defer func() {
		if !ok {
			j.Close()
		}
	}()
	p := filepath.Join(dir, "identity.json")
	idFile, err := openPrivateFile(p, syscall.O_RDONLY)
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(idFile, 4097))
		idFile.Close()
		if len(b) > 4096 {
			return nil, fault("journal_corrupt", "identity record too large")
		}
	}
	if os.IsNotExist(err) {
		j.identity = journalIdentity{newID(), session, generation}
		f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(body(j.identity))
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
		if e = syncDir(dir); e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	} else {
		if err = json.Unmarshal(b, &j.identity); err != nil {
			return nil, fault("journal_corrupt", "invalid journal identity")
		}
		if j.identity.ID == "" || j.identity.Session != session || j.identity.Generation != generation {
			return nil, fault("identity_mismatch", "journal identity mismatch")
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.seg"))
	if err != nil {
		return nil, err
	}
	quarantines, _ := filepath.Glob(filepath.Join(dir, "*.torn-*"))
	for _, p := range quarantines {
		info, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() {
			return nil, fault("unsafe_path", "invalid quarantine file")
		}
		j.size += info.Size()
	}
	sort.Strings(files)
	for index, p := range files {
		ordinal, e := strconv.Atoi(strings.TrimSuffix(filepath.Base(p), ".seg"))
		if e != nil || ordinal != index+1 {
			return nil, fault("journal_corrupt", "segment order mismatch")
		}
		j.ordinal = ordinal
		if e = j.recover(p, index == len(files)-1); e != nil {
			return nil, e
		}
	}
	if err = j.newSegment(); err != nil {
		return nil, err
	}
	ok = true
	return j, nil
}

func (j *Journal) recover(path string, final bool) error {
	f, err := openPrivateFile(path, syscall.O_RDWR)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	j.size += info.Size()
	if j.size > j.cap {
		return fault("journal_full", "retained journal exceeds capacity")
	}
	header := make([]byte, len(segmentHeader))
	if _, err = io.ReadFull(f, header); err != nil || !bytes.Equal(header, segmentHeader) {
		return fault("journal_corrupt", "invalid segment header")
	}
	offset := int64(len(header))
	for {
		start := offset
		var length [4]byte
		n, e := io.ReadFull(f, length[:])
		if e == io.EOF && n == 0 {
			return nil
		}
		torn := e != nil
		var payload []byte
		var checksum [4]byte
		if !torn {
			count := binary.BigEndian.Uint32(length[:])
			if count == 0 || count > MaxFrame {
				return fault("journal_corrupt", "invalid record size")
			}
			payload = make([]byte, count)
			_, e = io.ReadFull(f, payload)
			torn = e != nil
			if !torn {
				_, e = io.ReadFull(f, checksum[:])
				torn = e != nil
			}
		}
		if torn {
			if !final {
				return fault("journal_corrupt", "incomplete non-final segment")
			}
			if _, err = f.Seek(start, io.SeekStart); err != nil {
				return err
			}
			tail, err := io.ReadAll(f)
			if err != nil {
				return err
			}
			q := path + ".torn-" + newID()
			out, err := os.OpenFile(q, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, err = out.Write(tail)
			if err == nil {
				err = out.Sync()
			}
			ce := out.Close()
			if err != nil {
				return err
			}
			if ce != nil {
				return ce
			}
			// Quarantined evidence counts toward the cap, even after truncation.
			if err = f.Truncate(start); err != nil {
				return err
			}
			if err = f.Sync(); err != nil {
				return err
			}
			return syncDir(j.dir)
		}
		if crc32.ChecksumIEEE(payload) != binary.BigEndian.Uint32(checksum[:]) {
			return fault("journal_corrupt", "record checksum mismatch")
		}
		var event mesh.Event
		if err = json.Unmarshal(payload, &event); err != nil {
			return fault("journal_corrupt", "invalid event JSON")
		}
		want := j.cursor(uint64(len(j.events) + 1))
		if event.Cursor != want || event.Validate() != nil {
			return fault("journal_corrupt", "invalid event envelope or cursor order")
		}
		j.events = append(j.events, event)
		offset += int64(8 + len(payload))
	}
}

func (j *Journal) newSegment() error {
	if j.size+int64(len(segmentHeader)) > j.cap {
		return fault("journal_full", "journal capacity exhausted")
	}
	if j.file != nil {
		if err := j.file.Close(); err != nil {
			return err
		}
	}
	j.ordinal++
	f, err := os.OpenFile(filepath.Join(j.dir, fmt.Sprintf("%08d.seg", j.ordinal)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	j.file = f
	if _, err = f.Write(segmentHeader); err != nil {
		return err
	}
	if err = j.syncFile(f); err != nil {
		return err
	}
	if err = syncDir(j.dir); err != nil {
		return err
	}
	j.segmentSize = int64(len(segmentHeader))
	j.size += j.segmentSize
	return nil
}
func (j *Journal) cursor(seq uint64) string { return j.identity.ID + ":" + strconv.FormatUint(seq, 10) }
func (j *Journal) parse(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	prefix := j.identity.ID + ":"
	if !strings.HasPrefix(cursor, prefix) {
		return 0, fault("cursor_invalid", "cursor belongs to another journal")
	}
	seq, err := strconv.ParseUint(strings.TrimPrefix(cursor, prefix), 10, 64)
	if err != nil || seq > uint64(len(j.events)) || j.cursor(seq) != cursor {
		return 0, fault("cursor_invalid", "cursor outside durable history")
	}
	return seq, nil
}
func (j *Journal) Append(event mesh.Event, terminal bool) (mesh.Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return mesh.Event{}, fault("journal_unavailable", "journal closed")
	}
	if j.failure != nil {
		return mesh.Event{}, j.failure
	}
	seq := uint64(len(j.events) + 1)
	event.Cursor = j.cursor(seq)
	event.SourceSequence = seq
	if err := event.Validate(); err != nil {
		return mesh.Event{}, err
	}
	b, err := json.Marshal(event)
	if err != nil {
		return mesh.Event{}, err
	}
	if len(b) > MaxFrame {
		return mesh.Event{}, fault("invalid_request", "journal record too large")
	}
	needed := int64(len(b) + 8)
	limit := j.cap
	if !terminal {
		limit -= terminalReserve
	}
	if j.size+needed+int64(len(segmentHeader)) > limit {
		return mesh.Event{}, fault("journal_full", "journal capacity exhausted")
	}
	if j.segmentSize+needed > segmentLimit {
		if err = j.newSegment(); err != nil {
			j.failure = err
			return mesh.Event{}, err
		}
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint32(len(b)))
	buf.Write(b)
	binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(b))
	if _, err = j.file.Write(buf.Bytes()); err == nil {
		err = j.syncFile(j.file)
	}
	if err != nil {
		j.failure = fault("journal_unavailable", "append or fsync failed")
		return mesh.Event{}, j.failure
	}
	j.size += needed
	j.segmentSize += needed
	var owned mesh.Event
	json.Unmarshal(b, &owned)
	event = owned
	j.events = append(j.events, event)
	for sub := range j.subscribers {
		select {
		case sub.queue <- event:
		default:
			close(sub.done)
			delete(j.subscribers, sub)
		}
	}
	return event, nil
}

// Subscribe takes the high-water snapshot and installs the tail queue under
// the append lock. A slow subscriber is closed rather than blocking appends.
func (j *Journal) Subscribe(after string, queueSize int) ([]mesh.Event, *subscription, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	seq, err := j.parse(after)
	if err != nil {
		return nil, nil, err
	}
	s := &subscription{make(chan mesh.Event, queueSize), make(chan struct{})}
	j.subscribers[s] = struct{}{}
	snapshot := append([]mesh.Event(nil), j.events[seq:]...)
	return snapshot, s, nil
}
func (j *Journal) unsubscribe(s *subscription) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.subscribers[s]; ok {
		delete(j.subscribers, s)
		close(s.done)
	}
}
func (j *Journal) ValidateCursor(cursor string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, err := j.parse(cursor)
	return err
}
func (j *Journal) Snapshot() []mesh.Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]mesh.Event(nil), j.events...)
}
func (j *Journal) HighWater() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursor(uint64(len(j.events)))
}
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for sub := range j.subscribers {
		close(sub.done)
		delete(j.subscribers, sub)
	}
	var err error
	if j.file != nil {
		err = j.file.Close()
		j.file = nil
	}
	if j.lock != nil {
		j.lock.Close()
		j.lock = nil
	}
	return err
}
