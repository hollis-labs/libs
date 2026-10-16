package logkit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ErrMarkerUnreadable means a marker file exists but cannot be parsed. A log
// must treat it as a storage mismatch, not as an absent marker.
var ErrMarkerUnreadable = errors.New("logkit: high-water marker unreadable")

// Marker is the high-water record a log keeps OUTSIDE its storage. It is
// written after commits, so it is a lower bound on the stored heads: a store
// whose head is below the marker has been rolled back or replaced. A stale
// marker only weakens detection; it never causes a false mismatch.
type Marker struct {
	Epoch   string            `json:"epoch"`
	Streams map[string]uint64 `json:"streams"`
}

// ReadMarker reads a marker file. A missing file returns (nil, nil).
func ReadMarker(path string) (*Marker, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, ErrMarkerUnreadable
	}
	return &m, nil
}

// WriteMarker writes the marker atomically (temp file, fsync, rename).
func WriteMarker(path string, m Marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(name)
		return werr
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// Behind reports the first stream whose stored head is below the marker's, if any.
func (m *Marker) Behind(heads map[string]uint64) (stream string, behind bool) {
	if m == nil {
		return "", false
	}
	for s, hw := range m.Streams {
		if heads[s] < hw {
			return s, true
		}
	}
	return "", false
}
