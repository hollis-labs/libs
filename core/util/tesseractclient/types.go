package tesseract

import "encoding/json"

// RecallFilters is the nested `filters` object of a recall request. It
// mirrors a subset of the server's memory.RecallFilters, and only fields the
// server's recall route accepts.
//
// The JSON keys are capitalized Go field names, not snake_case, on purpose:
// the server decodes this object into a struct with no JSON tags, and
// encoding/json matches case-insensitively but not across underscores, so
// "confidence_min" would be rejected as an unknown field. Do not "fix" the
// tags.
//
// The server's struct has more fields (DerivedFrom, SimilarityMin, Domains,
// FacetKinds, FacetSources, RelatedTo, RelatedRelations, PointerHealth,
// state_filters) that no caller here populates; add one only by copying the
// server's exact key, and update the pinned key list in filters_test.go. A
// field the server does not have (Tangent's old Origins) would make every
// recall that set it fail with a 400, and must never be added.
type RecallFilters struct {
	// Statuses restricts results to these revision statuses.
	Statuses []string `json:"Statuses,omitempty"`
	// Tags restricts results to revisions carrying these tags.
	Tags []string `json:"Tags,omitempty"`
	// ConfidenceMin is a floor on the confidence the author recorded.
	ConfidenceMin float64 `json:"ConfidenceMin,omitempty"`
	// Since is an inclusive RFC 3339 lower bound. It is a string here and
	// decodes into a time on the server; this package only passes it through.
	Since string `json:"Since,omitempty"`
	// Until is an RFC 3339 upper bound; see Since.
	Until string `json:"Until,omitempty"`
}

// RecallRequest is one recall. The server accepts search_mode and filters
// together; either, both or neither may be set.
type RecallRequest struct {
	// Namespaces to search. A trailing "/*" expands to sub-namespaces.
	Namespaces []string `json:"namespaces"`
	// Ranking is "activation", "chronological", "relevance" or "similarity".
	Ranking string `json:"ranking,omitempty"`
	// Query is the search text, used under relevance ranking.
	Query string `json:"query,omitempty"`
	// SearchMode is "hybrid", "lexical" or "semantic" under relevance ranking.
	SearchMode string `json:"search_mode,omitempty"`
	// Filters narrows the candidate set. Omitted from the request when zero.
	Filters RecallFilters `json:"filters,omitzero"`
	// Limit is the page size; zero means the server default.
	Limit int `json:"limit,omitempty"`
	// PayloadMode is "keys", "summary" or "full"; empty means the server default.
	PayloadMode string `json:"payload_mode,omitempty"`
	// Cursor continues from Manifest.NextCursor of a previous page.
	Cursor string `json:"cursor,omitempty"`
}

// Manifest is recall's own account of what it did and did not return.
// Truncated=false means everything matched came back; completeness is never
// inferred from the length of Results.
type Manifest struct {
	ResultsTotal     int    `json:"results_total"`
	ResultsReturned  int    `json:"results_returned"`
	BytesReturned    int    `json:"bytes_returned,omitempty"`
	TokensEstimate   int    `json:"tokens_estimate,omitempty"`
	Truncated        bool   `json:"truncated"`
	TruncationReason string `json:"truncation_reason,omitempty"`
	NextCursor       string `json:"next_cursor,omitempty"`
}

// Revision is one Tesseract revision as the read routes return it: the union
// of the fields Station and Tangent read. A field a caller ignores costs
// nothing at its zero value. Payload.Data and ConsumerState stay raw: what
// they mean belongs to the caller.
type Revision struct {
	RevisionID string   `json:"revision_id"`
	ItemID     string   `json:"item_id,omitempty"`
	MemoryID   string   `json:"memory_id,omitempty"`
	Domain     string   `json:"domain"`
	Namespace  string   `json:"namespace"`
	MemoryKey  string   `json:"memory_key"`
	Status     string   `json:"status"`
	Supersedes string   `json:"supersedes,omitempty"`
	CreatedAt  string   `json:"created_at"`
	SessionID  string   `json:"session_id,omitempty"`
	Confidence float64  `json:"confidence"`
	Tags       []string `json:"tags"`
	Author     struct {
		AgentID      string `json:"agent_id"`
		AgentVersion string `json:"agent_version"`
	} `json:"author,omitzero"`
	Payload struct {
		Summary string          `json:"summary"`
		Body    string          `json:"body,omitempty"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"payload"`
	Facets struct {
		Kind    string `json:"kind"`
		Source  string `json:"source"`
		Pointer *struct {
			Scheme  string `json:"scheme"`
			Locator string `json:"locator"`
		} `json:"pointer,omitempty"`
	} `json:"facets,omitzero"`
	ConsumerState json.RawMessage `json:"consumer_state,omitempty"`
}

// Result is one recall hit. Score is nil when the ranking carries none.
type Result struct {
	Revision Revision `json:"revision"`
	Score    *float64 `json:"score"`
}

// RecallPage is one page of a recall.
type RecallPage struct {
	Results  []Result `json:"results"`
	Manifest Manifest `json:"manifest"`
}
