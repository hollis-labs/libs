package tether

// workstream_client.go — workstreams: a workstream groups the sessions of one
// line of work across compactions, and the digest routes roll up what those
// sessions touched. The methods sit flat on Client, matching the daemon's own
// route layout (/workstreams and /sessions/{id}/...).

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Workstream is a workstream as the daemon serves it.
type Workstream struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type workstreamListResponse struct {
	Workstreams []Workstream `json:"workstreams"`
}

type workstreamCreateRequest struct {
	Name       string `json:"name,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
}

type sessionWorkstreamRequest struct {
	WorkstreamID string `json:"workstream_id,omitempty"`
	Ensure       bool   `json:"ensure,omitempty"`
	Name         string `json:"name,omitempty"`
	WorkflowID   string `json:"workflow_id,omitempty"`
}

// WorkstreamNamespaceResponse says where a workstream's contained content
// belongs in Tesseract's workspace. The daemon stores none of it.
type WorkstreamNamespaceResponse struct {
	Namespace    string `json:"namespace"`
	WorkstreamID string `json:"workstream_id"`
}

// SessionWorkstreamNamespaceOptions are the optional placement overrides for
// SessionWorkstreamNamespace.
type SessionWorkstreamNamespaceOptions struct {
	Project string
	Owner   string
	Tail    string
}

// DigestQuery filters a digest. The zero value asks for everything within the
// server's default limit. Since is an RFC3339 UTC lower bound.
type DigestQuery struct {
	Kind     string
	Relation string
	Source   string
	Since    string
	Limit    int
}

func (q DigestQuery) encode() url.Values {
	v := url.Values{}
	for k, s := range map[string]string{"kind": q.Kind, "relation": q.Relation, "source": q.Source, "since": q.Since} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return v
}

// DigestRef is one ref in a digest.
type DigestRef struct {
	SessionID string `json:"session_id"`
	RefID     string `json:"ref_id"`
	URI       string `json:"uri,omitempty"`
	Relation  string `json:"relation"`
	Source    string `json:"source"`
	At        string `json:"at"`
}

// DigestKindGroup is one kind's refs, split by relation. The digest carries
// these as an ordered array so the same input renders the same output.
type DigestKindGroup struct {
	Kind       string      `json:"kind"`
	Created    []DigestRef `json:"created,omitempty"`
	Updated    []DigestRef `json:"updated,omitempty"`
	Read       []DigestRef `json:"read,omitempty"`
	Referenced []DigestRef `json:"referenced,omitempty"`
}

// DigestSession is one session in a digest's span. RefAttribution is always
// present: "unknown" is an answer, not an absent value.
type DigestSession struct {
	ID              string `json:"id"`
	Intent          string `json:"intent"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	State           string `json:"state"`
	CreatedAt       string `json:"created_at"`
	EndedAt         string `json:"ended_at,omitempty"`
	RefAttribution  string `json:"ref_attribution"`
	RefCount        int    `json:"ref_count"`
}

// DigestSpan is which sessions the digest covers, always with the full roster
// (a one-session workstream is an answer too; SpansLineage says whether the
// roll-up crossed sessions at all).
type DigestSpan struct {
	SessionCount int             `json:"session_count"`
	SpansLineage bool            `json:"spans_lineage"`
	Sessions     []DigestSession `json:"sessions"`
}

// DigestTotals counts what the digest contains after filters and the limit.
type DigestTotals struct {
	Refs       int            `json:"refs"`
	ByRelation map[string]int `json:"by_relation"`
	BySource   map[string]int `json:"by_source"`
}

// DigestCoverage is what the digest did and did not see. Machine consumers
// read the fields; Note is prose for a human.
type DigestCoverage struct {
	Limit             int            `json:"limit"`
	Truncated         bool           `json:"truncated"`
	Attribution       map[string]int `json:"attribution"`
	ProxyAttributable int            `json:"proxy_attributable"`
	Note              string         `json:"note,omitempty"`
}

// DigestResponse is the digest at either grain: one session (Grain "session",
// SessionID set) or a whole workstream (Workstream set).
type DigestResponse struct {
	Grain      string            `json:"grain"`
	SessionID  string            `json:"session_id,omitempty"`
	Workstream *Workstream       `json:"workstream,omitempty"`
	Span       DigestSpan        `json:"span"`
	LeftBehind []DigestKindGroup `json:"left_behind"`
	Touched    []DigestKindGroup `json:"touched"`
	Totals     DigestTotals      `json:"totals"`
	Coverage   DigestCoverage    `json:"coverage"`
}

// CreateWorkstream creates a workstream via POST /workstreams.
func (c *Client) CreateWorkstream(ctx context.Context, name, workflowID string) (Workstream, error) {
	var out Workstream
	err := c.doJSON(ctx, http.MethodPost, "/workstreams", workstreamCreateRequest{Name: name, WorkflowID: workflowID}, http.StatusCreated, &out)
	return out, err
}

// GetWorkstream fetches one workstream via GET /workstreams/{id}.
func (c *Client) GetWorkstream(ctx context.Context, id string) (Workstream, error) {
	var out Workstream
	if err := c.getJSON(ctx, "/workstreams/"+url.PathEscape(id), &out); err != nil {
		return Workstream{}, err
	}
	return out, nil
}

// ListWorkstreams lists workstreams via GET /workstreams, optionally filtered
// by status and workflow.
func (c *Client) ListWorkstreams(ctx context.Context, status, workflowID string) ([]Workstream, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if workflowID != "" {
		q.Set("workflow_id", workflowID)
	}
	var res workstreamListResponse
	if err := c.getJSON(ctx, withQuery("/workstreams", q), &res); err != nil {
		return nil, err
	}
	return res.Workstreams, nil
}

// AssignSessionWorkstream stamps a workstream onto a session via
// POST /sessions/{id}/workstream. An empty workstreamID clears it.
func (c *Client) AssignSessionWorkstream(ctx context.Context, sessionID, workstreamID string) error {
	return c.doNoBody(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/workstream",
		sessionWorkstreamRequest{WorkstreamID: workstreamID}, http.StatusNoContent)
}

// EnsureSessionWorkstream returns the session's workstream, creating one for
// its lineage when it has none.
func (c *Client) EnsureSessionWorkstream(ctx context.Context, sessionID, name, workflowID string) (Workstream, error) {
	var out Workstream
	err := c.doJSON(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/workstream",
		sessionWorkstreamRequest{Ensure: true, Name: name, WorkflowID: workflowID}, http.StatusOK, &out)
	return out, err
}

// SessionWorkstreamNamespace asks the daemon where a session's workstream-scoped
// content belongs in Tesseract's workspace. The daemon returns the location and
// stores nothing; the caller writes to Tesseract itself.
func (c *Client) SessionWorkstreamNamespace(ctx context.Context, sessionID string, opts ...SessionWorkstreamNamespaceOptions) (WorkstreamNamespaceResponse, error) {
	q := url.Values{}
	if len(opts) > 0 {
		o := opts[0]
		for k, v := range map[string]string{"project": o.Project, "owner": o.Owner, "tail": o.Tail} {
			if v != "" {
				q.Set(k, v)
			}
		}
	}
	var out WorkstreamNamespaceResponse
	if err := c.getJSON(ctx, withQuery("/sessions/"+url.PathEscape(sessionID)+"/workstream-namespace", q), &out); err != nil {
		return WorkstreamNamespaceResponse{}, err
	}
	return out, nil
}

// SessionDigest fetches GET /sessions/{id}/digest: one session's own refs,
// split into what it left behind and what it only consulted. It does not roll
// up the lineage; WorkstreamDigest does.
func (c *Client) SessionDigest(ctx context.Context, sessionID string, q DigestQuery) (DigestResponse, error) {
	var out DigestResponse
	if err := c.getJSON(ctx, withQuery("/sessions/"+url.PathEscape(sessionID)+"/digest", q.encode()), &out); err != nil {
		return DigestResponse{}, err
	}
	return out, nil
}

// WorkstreamDigest fetches GET /workstreams/{id}/digest: the roll-up across
// every session in the workstream.
func (c *Client) WorkstreamDigest(ctx context.Context, workstreamID string, q DigestQuery) (DigestResponse, error) {
	var out DigestResponse
	if err := c.getJSON(ctx, withQuery("/workstreams/"+url.PathEscape(workstreamID)+"/digest", q.encode()), &out); err != nil {
		return DigestResponse{}, err
	}
	return out, nil
}

// WorkstreamsForRef fetches GET /workstreams?ref=<kind>:<ref_id>: the
// workstreams containing a session that touched this object. It returns every
// match, because two efforts touching the same object is ordinary.
func (c *Client) WorkstreamsForRef(ctx context.Context, selector string) ([]Workstream, error) {
	var res workstreamListResponse
	q := url.Values{}
	q.Set("ref", selector)
	if err := c.getJSON(ctx, "/workstreams?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	return res.Workstreams, nil
}
