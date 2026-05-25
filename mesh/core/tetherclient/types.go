package tether

const (
	DefaultListenAddr = "unix:~/.tether/run/muxd.sock"

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

	BootPrompt      string `json:"boot_prompt,omitempty"`
	AgentFile       string `json:"agent_file,omitempty"`
	AgentInline     string `json:"agent_inline,omitempty"`
	BootProfileFile string `json:"boot_profile,omitempty"`
	Override        string `json:"override,omitempty"`
	PromptAppend    string `json:"prompt_append,omitempty"`
	Injection       string `json:"injection,omitempty"`
}

type LaunchResponse struct {
	ID             string `json:"id"`
	Workspace      string `json:"workspace"`
	Log            string `json:"log"`
	ProviderID     string `json:"provider_id"`
	ProviderKind   string `json:"provider_kind,omitempty"`
	LogicalAgentID string `json:"logical_agent_id,omitempty"`
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
	ProviderKind    string  `json:"provider_kind,omitempty"`
	Workspace       string  `json:"workspace"`
	State           string  `json:"state"`
	PID             *int    `json:"pid,omitempty"`
	ExitCode        *int    `json:"exit_code,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	EndedAt         *string `json:"ended_at,omitempty"`
	AttachedClients int     `json:"attached_clients"`
	SessionGroupID  string  `json:"session_group_id,omitempty"`
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
	Limit    int
	Cursor   int64
	SinceSeq int64
	Scopes   []string
	Kinds    []string
}

type EventListResponse struct {
	Events     []Event `json:"events"`
	NextCursor int64   `json:"next_cursor,omitempty"`
}

type StreamEventsOptions struct {
	SinceSeq  int64
	Scopes    []string
	Kinds     []string
	SessionID string
}

type StreamEvent struct {
	Seq         int64
	Kind        string
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

type ListAIProvidersResponse struct {
	Providers []AIProvider `json:"providers"`
}

type AIProvider struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	DefaultModel string   `json:"default_model,omitempty"`
	Models       []string `json:"models,omitempty"`
	BaseURL      string   `json:"base_url,omitempty"`
}

type ListAIModelsResponse struct {
	Models []AIModel `json:"models"`
}

type AIModel struct {
	ConfiguredProviderID string   `json:"configured_provider_id"`
	VendorProviderID     string   `json:"vendor_provider_id"`
	ID                   string   `json:"id"`
	Name                 string   `json:"name,omitempty"`
	Family               string   `json:"family,omitempty"`
	ContextWindow        int      `json:"context_window,omitempty"`
	MaxOutputTokens      int      `json:"max_output_tokens,omitempty"`
	InputModalities      []string `json:"input_modalities,omitempty"`
	OutputModalities     []string `json:"output_modalities,omitempty"`
}

type ListAIRoutesResponse struct {
	Routes []AIRoute `json:"routes"`
}

type AIRoute struct {
	Provider          string               `json:"provider"`
	Model             string               `json:"model"`
	Mode              string               `json:"mode,omitempty"`
	Intent            string               `json:"intent,omitempty"`
	RequiresReasoning bool                 `json:"requires_reasoning,omitempty"`
	RequiresTools     bool                 `json:"requires_tools,omitempty"`
	AllowReasoning    *bool                `json:"allow_reasoning,omitempty"`
	AllowTools        *bool                `json:"allow_tools,omitempty"`
	AllowAttachments  *bool                `json:"allow_attachments,omitempty"`
	MaxOutputTokens   *int                 `json:"max_output_tokens,omitempty"`
	MaxCostUSD        *float64             `json:"max_cost_usd,omitempty"`
	UsageBudget       *AIUsageBudgetPolicy `json:"usage_budget,omitempty"`
}

type RoutePreviewResponse struct {
	Route AIRoutePreview `json:"route"`
}

type AIRoutePreview struct {
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	EstimatedCostUSD float64  `json:"estimated_cost_usd,omitempty"`
	Reasons          []string `json:"reasons,omitempty"`
	PolicyVersion    string   `json:"policy_version,omitempty"`
}

type RouteExplainResponse struct {
	PolicyVersion string                    `json:"policy_version,omitempty"`
	Winner        *AIRoutePreview           `json:"winner,omitempty"`
	Error         string                    `json:"error,omitempty"`
	Candidates    []AIRouteExplainCandidate `json:"candidates"`
}

type AIRouteExplainCandidate struct {
	Provider          string               `json:"provider"`
	Model             string               `json:"model"`
	Mode              string               `json:"mode,omitempty"`
	Intent            string               `json:"intent,omitempty"`
	RequiresReasoning bool                 `json:"requires_reasoning,omitempty"`
	RequiresTools     bool                 `json:"requires_tools,omitempty"`
	AllowReasoning    *bool                `json:"allow_reasoning,omitempty"`
	AllowTools        *bool                `json:"allow_tools,omitempty"`
	AllowAttachments  *bool                `json:"allow_attachments,omitempty"`
	MaxOutputTokens   *int                 `json:"max_output_tokens,omitempty"`
	MaxCostUSD        *float64             `json:"max_cost_usd,omitempty"`
	UsageBudget       *AIUsageBudgetPolicy `json:"usage_budget,omitempty"`
	Matched           bool                 `json:"matched"`
	Selected          bool                 `json:"selected,omitempty"`
	Reasons           []string             `json:"reasons,omitempty"`
	Error             string               `json:"error,omitempty"`
	EstimatedCostUSD  float64              `json:"estimated_cost_usd,omitempty"`
}

type AIUsageBudgetPolicy struct {
	Level      string   `json:"level,omitempty"`
	MaxCostUSD *float64 `json:"max_cost_usd,omitempty"`
	Window     string   `json:"window,omitempty"`
	Scope      string   `json:"scope,omitempty"`
}

type ChatRequest struct {
	Request AIRequest `json:"request"`
}

type ChatResponse struct {
	Response AIResponse `json:"response"`
}

type AIRequest struct {
	Operation       string             `json:"operation"`
	ProviderHint    string             `json:"provider_hint,omitempty"`
	ModelHint       string             `json:"model_hint,omitempty"`
	Mode            string             `json:"mode,omitempty"`
	Intent          string             `json:"intent,omitempty"`
	RequestID       string             `json:"request_id,omitempty"`
	SessionID       string             `json:"session_id,omitempty"`
	CallerID        string             `json:"caller_id,omitempty"`
	Streaming       bool               `json:"streaming,omitempty"`
	MaxInputTokens  int                `json:"max_input_tokens,omitempty"`
	MaxOutputTokens int                `json:"max_output_tokens,omitempty"`
	TokenBudget     int                `json:"token_budget,omitempty"`
	CostBudgetUSD   float64            `json:"cost_budget_usd,omitempty"`
	LatencyTargetMS int                `json:"latency_target_ms,omitempty"`
	Input           []AIMessage        `json:"input,omitempty"`
	Tools           []AIToolDefinition `json:"tools,omitempty"`
	Attachments     []AIAttachment     `json:"attachments,omitempty"`
	Metadata        map[string]string  `json:"metadata,omitempty"`
}

type AIMessage struct {
	Role    string          `json:"role"`
	Parts   []AIContentPart `json:"parts,omitempty"`
	Name    string          `json:"name,omitempty"`
	ToolUse *AIToolUse      `json:"tool_use,omitempty"`
}

type AIContentPart struct {
	Type     string `json:"type"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Data     []byte `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
	Name     string `json:"name,omitempty"`
}

type AIToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SchemaJSON  string `json:"schema_json,omitempty"`
}

type AIToolUse struct {
	Name       string `json:"name"`
	Arguments  string `json:"arguments,omitempty"`
	Invocation string `json:"invocation,omitempty"`
}

type AIAttachment struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type AIResponse struct {
	Provider   string          `json:"provider,omitempty"`
	Model      string          `json:"model,omitempty"`
	Output     []AIMessage     `json:"output,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Refusal    string          `json:"refusal,omitempty"`
	Usage      AIUsage         `json:"usage,omitempty"`
	Route      AIRouteDecision `json:"route,omitempty"`
}

type AIRouteDecision struct {
	Provider      string   `json:"provider,omitempty"`
	Model         string   `json:"model,omitempty"`
	Reasons       []string `json:"reasons,omitempty"`
	PolicyVersion string   `json:"policy_version,omitempty"`
}

type AIUsage struct {
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int     `json:"reasoning_tokens,omitempty"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
}

type AIStreamEvent struct {
	Kind       string      `json:"kind"`
	Provider   string      `json:"provider,omitempty"`
	Model      string      `json:"model,omitempty"`
	Delta      string      `json:"delta,omitempty"`
	ToolUse    *AIToolUse  `json:"tool_use,omitempty"`
	Error      string      `json:"error,omitempty"`
	StopReason string      `json:"stop_reason,omitempty"`
	Usage      AIUsage     `json:"usage,omitempty"`
	Response   *AIResponse `json:"response,omitempty"`
}

type AIAuditQuery struct {
	EventType  string
	Provider   string
	Model      string
	SessionID  string
	CallerID   string
	Limit      int
	Since      string
	ErrorsOnly bool
}

type AIAuditEvent struct {
	ID               int64   `json:"id"`
	EventType        string  `json:"event_type"`
	RequestID        string  `json:"request_id,omitempty"`
	SessionID        string  `json:"session_id,omitempty"`
	CallerID         string  `json:"caller_id,omitempty"`
	Operation        string  `json:"operation"`
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
	PolicyVersion    string  `json:"policy_version,omitempty"`
	LatencyMs        int64   `json:"latency_ms"`
	Success          bool    `json:"success"`
	Refusal          string  `json:"refusal,omitempty"`
	Error            string  `json:"error,omitempty"`
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int     `json:"reasoning_tokens,omitempty"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
	RequestSummary   string  `json:"request_summary,omitempty"`
	ResponseSummary  string  `json:"response_summary,omitempty"`
	Timestamp        string  `json:"timestamp"`
}

type AIAuditResponse struct {
	Events []AIAuditEvent `json:"events"`
	Count  int            `json:"count"`
}

type AIUsageQuery struct {
	Provider  string
	Model     string
	SessionID string
	CallerID  string
	Operation string
	Since     string
}

type AIUsageBreakdown struct {
	Key              string  `json:"key"`
	Requests         int     `json:"requests"`
	Successes        int     `json:"successes"`
	Errors           int     `json:"errors"`
	LatencyMs        int64   `json:"latency_ms"`
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int     `json:"reasoning_tokens,omitempty"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
}

type AIUsageResponse struct {
	Requests         int                `json:"requests"`
	Successes        int                `json:"successes"`
	Errors           int                `json:"errors"`
	LatencyMs        int64              `json:"latency_ms"`
	InputTokens      int                `json:"input_tokens,omitempty"`
	OutputTokens     int                `json:"output_tokens,omitempty"`
	CacheReadTokens  int                `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int                `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int                `json:"reasoning_tokens,omitempty"`
	EstimatedCostUSD float64            `json:"estimated_cost_usd,omitempty"`
	ByProvider       []AIUsageBreakdown `json:"by_provider,omitempty"`
	ByModel          []AIUsageBreakdown `json:"by_model,omitempty"`
	ByOperation      []AIUsageBreakdown `json:"by_operation,omitempty"`
}

type AIBudgetsQuery struct {
	Provider  string
	Model     string
	SessionID string
	CallerID  string
}

type AIUsageBudgetFilter struct {
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	CallerID  string `json:"caller_id,omitempty"`
	Operation string `json:"operation,omitempty"`
}

type AIUsageBudgetEntry struct {
	Provider         string              `json:"provider"`
	Model            string              `json:"model"`
	Mode             string              `json:"mode,omitempty"`
	Intent           string              `json:"intent,omitempty"`
	UsageBudget      AIUsageBudgetPolicy `json:"usage_budget"`
	WindowStart      string              `json:"window_start"`
	SpentCostUSD     float64             `json:"spent_cost_usd,omitempty"`
	RemainingCostUSD float64             `json:"remaining_cost_usd,omitempty"`
	Exhausted        bool                `json:"exhausted"`
	Filter           AIUsageBudgetFilter `json:"filter"`
	Error            string              `json:"error,omitempty"`
}

type AIBudgetsResponse struct {
	Budgets []AIUsageBudgetEntry `json:"budgets"`
	Count   int                  `json:"count"`
}
