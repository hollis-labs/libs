package programstatus

import (
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

const payloadSchema = "urn:hollis-labs:events:session.status_changed@1"

// Event projects a status snapshot into the existing runtime event envelope.
// The caller supplies an event ID, time, session ID, and producer sequence in
// base. A durable log later assigns Cursor; a PTY scanner supplies rawOffset.
// Text from OSC is untrusted and classified private, never used as authority.
func (s Snapshot) Event(base runtimeevents.Event, rawOffset int64) (runtimeevents.Event, error) {
	if s.ProcessGeneration == 0 {
		return runtimeevents.Event{}, fmt.Errorf("programstatus: missing process generation")
	}
	liveness := "dead"
	if s.Alive {
		liveness = "alive"
	}
	work := string(s.Work)
	if work == "" {
		work = "unknown"
	}
	attention := s.Attention
	if attention == "" {
		attention = "unknown"
	}
	payload, err := json.Marshal(struct {
		Liveness          string `json:"liveness"`
		Work              string `json:"work"`
		Attention         string `json:"attention"`
		ProcessGeneration uint64 `json:"process_generation"`
		SourceSeq         uint64 `json:"source_seq"`
		RecordID          string `json:"record_id,omitempty"`
		Message           string `json:"message,omitempty"`
	}{liveness, work, attention, s.ProcessGeneration, s.SourceSeq, s.RecordID, s.Message})
	if err != nil {
		return runtimeevents.Event{}, err
	}
	base.Kind = runtimeevents.KindSessionStatusChanged
	base.Payload = payload
	base.Cursor = ""
	base.RecordedAt = nil
	base.RawOffset = nil
	if rawOffset >= 0 {
		base.RawOffset = &rawOffset
	}
	base.Source = runtimeevents.Source{Channel: runtimeevents.ChannelPTY, Confidence: runtimeevents.ConfidenceDerived}
	base.Class = &runtimeevents.Class{Category: "state", Sensitivity: "private", PayloadSchema: payloadSchema}
	if err := base.Validate(); err != nil {
		return runtimeevents.Event{}, err
	}
	return base, nil
}
