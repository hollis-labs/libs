package tether

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

// EnvironmentEvent extends the mesh envelope with the durable environment
// coordinates. Seq is unrelated to raw attach byte offsets or provider cursors.
type EnvironmentEvent struct {
	mesh.Event
	EnvironmentID string `json:"environment_id"`
	Seq           int64  `json:"seq"`
}

type EnvironmentSession struct {
	ID              string              `json:"id"`
	InstanceID      string              `json:"instance_id,omitempty"`
	LogicalAgentID  string              `json:"logical_agent_id,omitempty"`
	Kind            string              `json:"kind"`
	Route           json.RawMessage     `json:"route,omitempty"`
	SessionState    mesh.SessionState   `json:"session_state"`
	InstanceStatus  mesh.InstanceStatus `json:"instance_status"`
	InstanceDetail  mesh.InstanceDetail `json:"instance_detail"`
	LastActivity    time.Time           `json:"last_activity"`
	PendingQuestion bool                `json:"pending_question"`
	PendingApproval bool                `json:"pending_approval"`
	PendingKnown    bool                `json:"pending_known"`
	QuestionKnown   bool                `json:"pending_question_known"`
	ApprovalKnown   bool                `json:"pending_approval_known"`
}

type EnvironmentSnapshot struct {
	EnvironmentID     string               `json:"environment_id"`
	HighWaterSeq      int64                `json:"high_water_seq"`
	EarliestAvailable int64                `json:"earliest_available"`
	Sessions          []EnvironmentSession `json:"sessions"`
}

type EnvironmentGap struct {
	EnvironmentID     string `json:"environment_id"`
	Reason            string `json:"reason"`
	EarliestAvailable int64  `json:"earliest_available"`
	HighWaterSeq      int64  `json:"high_water_seq"`
	SnapshotRequired  bool   `json:"snapshot_required"`
}

func environmentStreamPath(sessionID, suffix string) string {
	if sessionID == "" {
		if suffix == "stream" {
			return "/environment/events"
		}
		return "/environment/" + suffix
	}
	return "/sessions/" + url.PathEscape(sessionID) + "/" + suffix
}

// Snapshot uses the same authenticated, verified route as this connection.
// Session snapshots still carry the global environment high-water cursor.
func (c *EnvironmentConnection) Snapshot(ctx context.Context, sessionID string) (EnvironmentSnapshot, error) {
	resp, err := environmentGET(ctx, c.Client.http, c.Client.baseURL+environmentStreamPath(sessionID, "snapshot"))
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EnvironmentSnapshot{}, environmentStatusError(resp, c.token())
	}
	var wire struct {
		EnvironmentSnapshot
		HighWater *int64 `json:"high_water_seq"`
		Earliest  *int64 `json:"earliest_available"`
	}
	if err := decodeEnvironmentJSON(resp.Body, &wire); err != nil {
		return EnvironmentSnapshot{}, err
	}
	snapshot := wire.EnvironmentSnapshot
	if snapshot.EnvironmentID != c.Descriptor.EnvironmentID {
		return snapshot, &EnvironmentIdentityError{}
	}
	if wire.HighWater == nil || wire.Earliest == nil || *wire.HighWater < 0 || *wire.Earliest < 0 {
		return snapshot, errors.New("tether: invalid snapshot cursor")
	}
	snapshot.HighWaterSeq, snapshot.EarliestAvailable = *wire.HighWater, *wire.Earliest
	for _, session := range snapshot.Sessions {
		if session.ID == "" || (sessionID != "" && session.ID != sessionID) {
			return snapshot, errors.New("tether: invalid snapshot session")
		}
	}
	return snapshot, nil
}

func (c *EnvironmentConnection) token() string {
	if transport, ok := c.Client.http.Transport.(*environmentTransport); ok {
		return transport.token
	}
	return ""
}

type environmentFrame struct {
	ready        bool
	event        *EnvironmentEvent
	gap          *EnvironmentGap
	synchronized *int64
}

// readEnvironmentStream waits indefinitely for stream data using the caller's
// context. Only establishment has a deadline. A parser cannot outrun consumer
// delivery: frames apply backpressure rather than silently dropping events.
func (e *EnvironmentClient) readEnvironmentStream(ctx context.Context, conn *EnvironmentConnection, sessionID string, after int64, emit func(environmentFrame) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(e.opts.ConnectTimeout, cancel)
	address := conn.Client.baseURL + environmentStreamPath(sessionID, "stream") + "?after_seq=" + strconv.FormatInt(after, 10)
	resp, err := environmentGET(ctx, conn.Client.longLivedClient(), address)
	timer.Stop()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return environmentStatusError(resp, conn.token())
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return errors.New("tether: invalid environment stream content type")
	}
	if err := emit(environmentFrame{ready: true}); err != nil {
		return err
	}
	return parseEnvironmentStream(ctx, resp.Body, conn.Descriptor.EnvironmentID, sessionID, after, emit)
}

func parseEnvironmentStream(ctx context.Context, reader io.Reader, environmentID, sessionID string, after int64, emit func(environmentFrame) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	var eventName, id string
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			eventName, id = "", ""
			return nil
		}
		var header struct {
			EnvironmentID string `json:"environment_id"`
			Kind          string `json:"kind"`
		}
		if json.Unmarshal([]byte(data.String()), &header) != nil {
			return errors.New("tether: invalid environment stream frame")
		}
		if header.EnvironmentID != environmentID {
			return &EnvironmentIdentityError{}
		}
		var frame environmentFrame
		switch {
		case eventName == "gap" && header.Kind == "":
			var gap EnvironmentGap
			if json.Unmarshal([]byte(data.String()), &gap) != nil || !gap.SnapshotRequired || gap.HighWaterSeq < 0 || gap.EarliestAvailable < 0 || gap.Reason == "" {
				return errors.New("tether: invalid environment gap")
			}
			frame.gap = &gap
		case eventName == "synchronized" && header.Kind == "":
			var marker struct {
				Seq *int64 `json:"seq"`
			}
			if json.Unmarshal([]byte(data.String()), &marker) != nil || marker.Seq == nil || *marker.Seq < 0 || id != strconv.FormatInt(*marker.Seq, 10) {
				return errors.New("tether: invalid synchronization cursor")
			}
			if *marker.Seq >= after {
				after = *marker.Seq
				frame.synchronized = marker.Seq
			}
		default:
			var event EnvironmentEvent
			if json.Unmarshal([]byte(data.String()), &event) != nil || event.Seq < 1 || event.ID == "" || event.Kind == "" ||
				id != strconv.FormatInt(event.Seq, 10) || (sessionID != "" && event.SessionID != sessionID) || (eventName != "" && eventName != event.Kind) {
				return errors.New("tether: invalid environment event coordinates")
			}
			if event.Seq > after {
				after = event.Seq
				frame.event = &event
			}
		}
		eventName, id = "", ""
		data.Reset()
		if frame.event != nil || frame.gap != nil || frame.synchronized != nil {
			return emit(frame)
		}
		return nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventName = value
		case "id":
			id = value
		case "data":
			if data.Len()+len(value)+1 > 16<<20 {
				return errors.New("tether: environment stream frame too large")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanner.Err() != nil {
		return errors.New("tether: environment stream interrupted")
	}
	// SSE dispatches only complete frames; a truncated last frame is replayed.
	return io.EOF
}
