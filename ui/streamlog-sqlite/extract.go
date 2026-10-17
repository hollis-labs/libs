package streamlogsqlite

import (
	"encoding/json"

	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// Columns are the indexed, low-cardinality fields stored beside each record.
// They exist so consumers can query by scope without parsing payloads; the
// record's Data stays the source of truth. All fields are optional.
type Columns struct {
	Category    string
	Severity    string
	Sensitivity string
	PII         bool
	Visibility  string
	// Generation is the producer's restart counter, stored verbatim.
	Generation   int64
	ProjectID    string
	WorkstreamID string
	TaskID       string
	SessionID    string
	AgentURN     string
	RunID        string
}

// Extractor lifts [Columns] out of an event. It must be pure and cheap, and
// must not fail: return zero Columns for data it does not understand.
type Extractor func(streamhub.Event) Columns

// EnvelopeExtractor reads the platform envelope shape from JSON data:
// top-level session_id and generation (and visibility, as mesh.Event keeps
// it), a "scope" block (project_id,
// workstream_id, task_id, run_id, agent_urn) and a "class" block (category,
// severity, sensitivity, pii, visibility; class.visibility wins over the
// top-level field). Unknown or malformed data yields
// zero Columns.
func EnvelopeExtractor(e streamhub.Event) Columns {
	var env struct {
		SessionID  string `json:"session_id"`
		Generation int64  `json:"generation"`
		Visibility string `json:"visibility"` // mesh.Event keeps it top-level
		Scope      struct {
			ProjectID    string `json:"project_id"`
			WorkstreamID string `json:"workstream_id"`
			TaskID       string `json:"task_id"`
			RunID        string `json:"run_id"`
			AgentURN     string `json:"agent_urn"`
		} `json:"scope"`
		Class struct {
			Category    string `json:"category"`
			Severity    string `json:"severity"`
			Sensitivity string `json:"sensitivity"`
			PII         bool   `json:"pii"`
			Visibility  string `json:"visibility"`
		} `json:"class"`
	}
	if len(e.Data) == 0 || json.Unmarshal(e.Data, &env) != nil {
		return Columns{}
	}
	vis := env.Class.Visibility
	if vis == "" {
		vis = env.Visibility
	}
	return Columns{
		Category: env.Class.Category, Severity: env.Class.Severity,
		Sensitivity: env.Class.Sensitivity, PII: env.Class.PII, Visibility: vis,
		Generation: env.Generation, ProjectID: env.Scope.ProjectID,
		WorkstreamID: env.Scope.WorkstreamID, TaskID: env.Scope.TaskID,
		SessionID: env.SessionID, AgentURN: env.Scope.AgentURN, RunID: env.Scope.RunID,
	}
}
