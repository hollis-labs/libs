package streamlogsqlite

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// marker is the high-water record kept outside the database file. It is
// written after commits, so it is always a lower bound on the database head:
// a database whose head is below the marker has been rolled back or replaced.
type marker struct {
	Epoch   string            `json:"epoch"`
	Streams map[string]uint64 `json:"streams"`
}

func readMarker(path string) (*marker, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m marker
	if err := json.Unmarshal(b, &m); err != nil {
		// An unreadable marker is treated as a mismatch by the caller.
		return &marker{Epoch: "\x00unreadable"}, nil
	}
	return &m, nil
}

func writeMarker(path string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
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
