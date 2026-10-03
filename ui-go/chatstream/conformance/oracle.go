package conformance

import (
	"encoding/json"
	"fmt"

	chatstream "github.com/hollis-labs/go-chatstream"
)

// CheckReplayEquivalence is the reducer oracle. For every cursor k it reduces
// events[:k], then applies events[k:] to that snapshot, and requires the result
// to equal reducing events whole. It also snapshots through JSON at k, as a
// client that stored the message would, and applies the rest to that. Any
// difference is returned.
func CheckReplayEquivalence(events []chatstream.Event) error {
	full, err := chatstream.Reduce(events, nil)
	if err != nil {
		return fmt.Errorf("reduce whole stream: %w", err)
	}
	want, err := canonical(full)
	if err != nil {
		return err
	}
	for k := 0; k <= len(events); k++ {
		head, err := chatstream.Reduce(events[:k], nil)
		if err != nil {
			return fmt.Errorf("reduce events[:%d]: %w", k, err)
		}
		rest, err := chatstream.Reduce(events[k:], head)
		if err != nil {
			return fmt.Errorf("apply events[%d:] to the snapshot: %w", k, err)
		}
		if got, _ := canonical(rest); got != want {
			return fmt.Errorf("replay from cursor %d differs from full replay:\n got %s\nwant %s", k, got, want)
		}

		stored, err := json.Marshal(head)
		if err != nil {
			return err
		}
		var restored chatstream.Message
		if uerr := json.Unmarshal(stored, &restored); uerr != nil {
			return uerr
		}
		viaJSON, err := chatstream.Reduce(events[k:], &restored)
		if err != nil {
			return fmt.Errorf("apply events[%d:] to the stored snapshot: %w", k, err)
		}
		if got, _ := canonical(viaJSON); got != want {
			return fmt.Errorf("replay from a stored snapshot at cursor %d differs from full replay:\n got %s\nwant %s", k, got, want)
		}
	}
	return nil
}

func canonical(m *chatstream.Message) (string, error) {
	b, err := json.Marshal(m)
	return string(b), err
}
