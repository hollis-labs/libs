package ledger

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"
)

// StageRetention is retention for one stage. Metadata, hashes, labels, corrections
// and outcomes are kept indefinitely; only the raw input snapshot is governed.
type StageRetention struct {
	Mode InputMode
	// RawTTL is how long a snapshot lives. Zero means it never expires; a
	// stage that holds sensitive text should not be zero.
	RawTTL time.Duration
	// Sample, if set, is the fraction of snapshots kept; the rest are stored
	// as a pointer (when one is given) or as a hash only. Nil keeps all.
	Sample *float64
	// Redact rewrites the snapshot before storage. It runs on the bytes that
	// will be stored; an error refuses the write rather than storing unredacted.
	Redact func(raw []byte) ([]byte, error)
}

// Retention applies stage rules when a Record is built.
type Retention struct {
	Default StageRetention
	Stages  map[Stage]StageRetention
	// HashKey, when set, makes input hashes HMACs.
	HashKey []byte
	Rand    func() float64 // tests; defaults to math/rand/v2
}

// DefaultRetention: snapshots for 30 days, everything else forever.
func DefaultRetention() Retention {
	return Retention{Default: StageRetention{Mode: ModeSnapshot, RawTTL: 30 * 24 * time.Hour}}
}

func (r Retention) rule(s Stage) StageRetention {
	if sr, ok := r.Stages[s]; ok {
		return sr
	}
	return r.Default
}

// Shape builds the Input for a record. raw is the input as JSON (callers pass
// what the decision actually saw); pointer says where the original lives, if
// anywhere; keep marks a training-set row that must not expire.
func (r Retention) Shape(stage Stage, raw json.RawMessage, pointer string, keep bool, now time.Time) (Input, error) {
	rule := r.rule(stage)
	in := Input{Mode: rule.Mode, Hash: HashInput(raw, r.HashKey), Keep: keep}
	if in.Mode == "" {
		in.Mode = ModeNone
	}
	if in.Mode == ModeSnapshot && rule.Sample != nil && !keep {
		roll := rand.Float64
		if r.Rand != nil {
			roll = r.Rand
		}
		if roll() >= *rule.Sample {
			in.Mode = ModeNone
			if pointer != "" {
				in.Mode = ModePointer
			}
		}
	}
	switch in.Mode {
	case ModeSnapshot:
		snap := []byte(raw)
		if rule.Redact != nil {
			var err error
			if snap, err = rule.Redact(snap); err != nil {
				return Input{}, fmt.Errorf("ledger: redact: %w", err)
			}
			in.Redacted = true
		}
		in.Snapshot = snap
		if rule.RawTTL > 0 && !keep {
			t := now.Add(rule.RawTTL)
			in.RawExpiresAt = &t
		}
		if pointer != "" {
			in.Pointer = pointer
		}
	case ModePointer:
		if pointer == "" {
			return Input{}, fmt.Errorf("ledger: stage %s is pointer mode but no pointer was given", stage)
		}
		in.Pointer = pointer
	}
	return in, nil
}
