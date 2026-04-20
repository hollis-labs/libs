package agentmux

const (
	DefaultListenAddr = "unix:~/.agent-mux/run/muxd.sock"

	ErrorCodeInvalidRequest   = "invalid_request"
	ErrorCodeNotFound         = "not_found"
	ErrorCodeMethodNotAllowed = "method_not_allowed"
	ErrorCodePayloadTooLarge  = "payload_too_large"
	ErrorCodeConflict         = "conflict"
	ErrorCodeInternalError    = "internal_error"
	ErrorCodeNotImplemented   = "not_implemented"

	ScopeSession = "session"
	ScopeDaemon  = "daemon"
	ScopeBroker  = "broker"
)

type Health struct {
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	UptimeSec int64  `json:"uptime_sec"`
	Listener  string `json:"listener"`
	Sessions  int    `json:"sessions"`
}

type LaunchRequest struct {
	Launch string `json:"launch"`
}

type LaunchResponse struct {
	ID         string `json:"id"`
	Workspace  string `json:"workspace"`
	Log        string `json:"log"`
	ProviderID string `json:"provider_id"`
}

type WaitResponse struct {
	ExitCode int `json:"exit_code"`
}

type ResizeRequest struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

type ListSessionsOptions struct {
	Limit  int
	Cursor string
	State  string
}

type ListSessionsResponse struct {
	Sessions   []Session `json:"sessions"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type Session struct {
	ID              string  `json:"id"`
	LaunchID        string  `json:"launch_id"`
	ProjectID       string  `json:"project_id"`
	LogicalAgentID  string  `json:"logical_agent_id"`
	ProviderID      string  `json:"provider_id"`
	Workspace       string  `json:"workspace"`
	State           string  `json:"state"`
	PID             *int    `json:"pid,omitempty"`
	ExitCode        *int    `json:"exit_code,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	EndedAt         *string `json:"ended_at,omitempty"`
	AttachedClients int     `json:"attached_clients"`
}

type CheckpointCreateRequest struct {
	TaskID              string `json:"task_id,omitempty"`
	WorkflowID          string `json:"workflow_id,omitempty"`
	Status              string `json:"status,omitempty"`
	CompletedWork       string `json:"completed_work,omitempty"`
	PendingWork         string `json:"pending_work,omitempty"`
	KeyDecisions        string `json:"key_decisions,omitempty"`
	ReferencedArtifacts string `json:"referenced_artifacts,omitempty"`
	Summary             string `json:"summary,omitempty"`
	NextRecommendation  string `json:"next_recommendation,omitempty"`
}

type Checkpoint struct {
	ID                  string `json:"id"`
	LogicalAgentID      string `json:"logical_agent_id"`
	TaskID              string `json:"task_id,omitempty"`
	WorkflowID          string `json:"workflow_id,omitempty"`
	Status              string `json:"status,omitempty"`
	CompletedWork       string `json:"completed_work,omitempty"`
	PendingWork         string `json:"pending_work,omitempty"`
	KeyDecisions        string `json:"key_decisions,omitempty"`
	ReferencedArtifacts string `json:"referenced_artifacts,omitempty"`
	Summary             string `json:"summary,omitempty"`
	NextRecommendation  string `json:"next_recommendation,omitempty"`
	CreatedAt           string `json:"created_at"`
	SourceSessionID     string `json:"source_session_id,omitempty"`
}

type CheckpointListResponse struct {
	Checkpoints []Checkpoint `json:"checkpoints"`
}

type EnvelopeCreateRequest struct {
	Sender        string `json:"sender,omitempty"`
	Recipient     string `json:"recipient,omitempty"`
	WorkflowID    string `json:"workflow_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	MessageType   string `json:"message_type,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	Payload       string `json:"payload,omitempty"`
	AuditJSON     string `json:"audit_json,omitempty"`
}

type Envelope struct {
	ID            string `json:"id"`
	Sender        string `json:"sender,omitempty"`
	Recipient     string `json:"recipient,omitempty"`
	WorkflowID    string `json:"workflow_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	MessageType   string `json:"message_type,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	Payload       string `json:"payload,omitempty"`
	CreatedAt     string `json:"created_at"`
	DeliveredAt   string `json:"delivered_at,omitempty"`
	ConsumedAt    string `json:"consumed_at,omitempty"`
	AuditJSON     string `json:"audit_json,omitempty"`
}

type EnvelopeListOptions struct {
	Recipient     string
	WorkflowID    string
	CorrelationID string
}

type EnvelopeListResponse struct {
	Envelopes []Envelope `json:"envelopes"`
}

type Event struct {
	Seq         int64  `json:"seq"`
	At          string `json:"at"`
	Scope       string `json:"scope"`
	SessionID   string `json:"session_id,omitempty"`
	Kind        string `json:"kind"`
	PayloadJSON string `json:"payload_json,omitempty"`
}

type EventListOptions struct {
	Limit  int
	Cursor int64
}

type EventListResponse struct {
	Events     []Event `json:"events"`
	NextCursor int64   `json:"next_cursor,omitempty"`
}

type StreamEventsOptions struct {
	SinceSeq  int64
	Scopes    []string
	SessionID string
}

type StreamEvent struct {
	ID          int64
	Event       string
	Scope       string
	SessionID   string
	PayloadJSON string
}

type Project struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	RepoRoot      string        `json:"repo_root"`
	TrackingRoot  string        `json:"tracking_root"`
	KnowledgeBase []string      `json:"knowledge_base,omitempty"`
	BootFragments []string      `json:"boot_fragments,omitempty"`
	Workspace     WorkspaceSpec `json:"workspace"`
}

type WorkspaceSpec struct {
	DefaultMode  string `json:"default_mode"`
	WorktreeBase string `json:"worktree_base,omitempty"`
	SessionRoot  string `json:"session_root,omitempty"`
}

type Agent struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Roles         []string         `json:"roles,omitempty"`
	Skills        []string         `json:"skills,omitempty"`
	ContextFiles  []string         `json:"context_files,omitempty"`
	BootFragments []string         `json:"boot_fragments,omitempty"`
	Permissions   AgentPermissions `json:"permissions"`
}

type AgentPermissions struct {
	Network        bool   `json:"network"`
	DefaultSandbox string `json:"default_sandbox,omitempty"`
}

type Provider struct {
	ID        string        `json:"id"`
	Type      string        `json:"type"`
	Command   string        `json:"command"`
	Args      []string      `json:"args,omitempty"`
	Bootstrap BootstrapSpec `json:"bootstrap"`
	Env       ProviderEnv   `json:"env"`
}

type BootstrapSpec struct {
	Mode         string `json:"mode,omitempty"`
	PromptPrefix string `json:"prompt_prefix,omitempty"`
}

type ProviderEnv struct {
	Mode        string   `json:"mode"`
	Passthrough []string `json:"passthrough,omitempty"`
	Redact      []string `json:"redact,omitempty"`
}

type Launch struct {
	ID        string          `json:"id"`
	Project   string          `json:"project"`
	Agent     string          `json:"agent"`
	Provider  string          `json:"provider"`
	Workspace LaunchWorkspace `json:"workspace"`
	Prompt    PromptSpec      `json:"prompt"`
	Overrides LaunchOverrides `json:"overrides"`
}

type LaunchWorkspace struct {
	Mode         string `json:"mode"`
	WorktreeName string `json:"worktree_name,omitempty"`
	WriteHome    string `json:"write_home,omitempty"`
}

type PromptSpec struct {
	IncludeProjectBoot   bool `json:"include_project_boot"`
	IncludeAgentBoot     bool `json:"include_agent_boot"`
	IncludeKnowledgeBase bool `json:"include_knowledge_base"`
}

type LaunchOverrides struct {
	Env map[string]string `json:"env,omitempty"`
}
