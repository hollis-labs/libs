package programstatus

import (
	"strings"
	"sync"
)

// Snapshot separates process liveness, reported work, and user attention.
// ProcessGeneration fences late PTY output from a replaced child. SourceSeq is
// only an observation order within that process; it is not a durable cursor or
// a PTY byte offset.
type Snapshot struct {
	ProcessGeneration uint64
	SourceSeq         uint64
	Alive             bool
	Work              State
	Attention         string
	RecordID          string
	Message           string
}

// Tracker keeps current status for one PTY. Its reports cannot establish
// authority: callers must bind the PTY and process generation themselves.
type Tracker struct {
	mu      sync.Mutex
	current Snapshot
	records map[string]Report
}

// Start establishes a new child process generation and retires earlier
// reports. Generation zero is reserved for unknown identity and is refused.
func (t *Tracker) Start(generation uint64) bool {
	if generation == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if generation <= t.current.ProcessGeneration {
		return false
	}
	t.current = Snapshot{ProcessGeneration: generation, Alive: true}
	t.records = make(map[string]Report)
	return true
}

// Apply accepts a parsed report only from the current live process and in
// strictly increasing source order. Clear removes the addressed subtree.
func (t *Tracker) Apply(generation, sourceSeq uint64, r Report) (Snapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.current.Alive || generation == 0 || generation != t.current.ProcessGeneration || sourceSeq <= t.current.SourceSeq {
		return t.current, false
	}
	t.current.SourceSeq = sourceSeq
	if r.State == Clear {
		for id := range t.records {
			if r.ID == "" || id == r.ID || strings.HasPrefix(id, r.ID+"/") {
				delete(t.records, id)
			}
		}
	} else {
		t.records[r.ID] = r
	}
	t.recompute()
	return t.current, true
}

// Exit fences the child immediately. Transient working/blocked reports expire;
// completed/failed reports remain visible until the next Start or Clear.
func (t *Tracker) Exit(generation uint64) (Snapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.current.Alive || generation == 0 || generation != t.current.ProcessGeneration {
		return t.current, false
	}
	t.current.Alive = false
	for id, r := range t.records {
		if r.State == Working || r.State == Blocked {
			delete(t.records, id)
		}
	}
	t.recompute()
	return t.current, true
}

// Snapshot returns the current projection without changing source order.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

func (t *Tracker) recompute() {
	t.current.Work, t.current.Attention, t.current.RecordID, t.current.Message = "", "", "", ""
	best := -1
	for id, r := range t.records {
		priority := -1
		switch r.State {
		case Blocked:
			priority = 5
		case Working:
			priority = 4
		case Error:
			priority = 3
		case Done:
			priority = 2
		case Idle:
			priority = 1
		}
		if priority < best || priority == best && id >= t.current.RecordID {
			continue
		}
		best = priority
		t.current.Work = r.State
		t.current.RecordID = id
		t.current.Message = r.Message
		switch r.State {
		case Blocked:
			t.current.Attention = r.Kind
			if t.current.Attention == "" {
				t.current.Attention = "user"
			}
		case Done, Error:
			t.current.Attention = string(r.State)
		default:
			t.current.Attention = "none"
		}
	}
}
