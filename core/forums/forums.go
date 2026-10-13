// Package forums defines the host-neutral threaded discussion boundary.
// Hosts supply authenticated callers and storage; consumers never supply provenance.
package forums

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalid   = errors.New("invalid forum request")
	ErrForbidden = errors.New("forum access denied")
	ErrNotFound  = errors.New("forum object not found")
	ErrConflict  = errors.New("forum object already exists")
)
var segment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func ValidateNamespace(ns, id string) error {
	p := strings.Split(ns, "/")
	if !segment.MatchString(id) || id == "topics" || id == "posts" || id == "categories" {
		return fmt.Errorf("%w: forum id is reserved", ErrInvalid)
	}
	if len(p) != 4 || p[0] != "app" || p[2] != "forum" || !segment.MatchString(p[1]) || !segment.MatchString(p[3]) {
		return fmt.Errorf("%w: namespace must be app/<consumer>/forum/<instance>", ErrInvalid)
	}
	return nil
}

type Principal struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}
type Provenance struct {
	Principal
	Verified     bool   `json:"verified"`
	SessionID    string `json:"session_id,omitempty"`
	AgentURN     string `json:"agent_urn,omitempty"`
	WorkstreamID string `json:"workstream_id,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	LaunchID     string `json:"launch_id,omitempty"`
}

// Caller is constructed by a host, never decoded from a request body.
type Caller struct {
	Provenance
	Operator bool
}

// Participant grants read access or read-and-post access to a stable principal.
type Participant struct {
	Principal
	Access string `json:"access"`
}

// ParticipantStore is an optional host capability; absent hosts retain owner-only policy.
type ParticipantStore interface {
	GetParticipant(context.Context, string, Principal) (Participant, error)
	ListParticipants(context.Context, string, Options) (Page[Participant], error)
	SetParticipant(context.Context, string, Participant) error
	RevokeParticipant(context.Context, string, Principal) error
	SetOwner(context.Context, string, Principal, string) (Forum, error)
}

type Policy struct {
	Mode string `json:"mode"`
}
type Category struct {
	SortOrder   int64  `json:"sort_order"`
	ID          string `json:"id"`
	Namespace   string `json:"namespace"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Archived    bool   `json:"archived"`
	OwnerType   string `json:"owner_type"`
	OwnerID     string `json:"owner_id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Forum struct {
	SortOrder   int64           `json:"sort_order"`
	CategoryID  string          `json:"category_id,omitempty"`
	Description string          `json:"description"`
	Archived    bool            `json:"archived"`
	ID          string          `json:"id"`
	Namespace   string          `json:"namespace"`
	Title       string          `json:"title"`
	OwnerType   string          `json:"owner_type"`
	OwnerID     string          `json:"owner_id"`
	Config      json.RawMessage `json:"config_json"`
	Policy      Policy          `json:"policy_json"`
	Metadata    json.RawMessage `json:"metadata_json"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	LastSeq     int64           `json:"last_seq"`
}
type Topic struct {
	SourceTopicID  string    `json:"source_topic_id,omitempty"`
	Archived       bool      `json:"archived"`
	ID             string    `json:"id"`
	ForumID        string    `json:"forum_id"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	CreatedBy      Principal `json:"created_by"`
	PostCount      int       `json:"post_count"`
	CreatedAt      string    `json:"created_at"`
	LastActivityAt string    `json:"last_activity_at"`
	LastSeq        int64     `json:"last_seq"`
}
type Attachment struct {
	Name      string `json:"name"`
	URI       string `json:"uri"`
	MediaType string `json:"media_type,omitempty"`
}

type Post struct {
	Subject      string       `json:"subject"`
	Attachments  []Attachment `json:"attachments"`
	SourcePostID string       `json:"source_post_id,omitempty"`
	ID           string       `json:"id"`
	TopicID      string       `json:"topic_id"`
	ForumID      string       `json:"forum_id"`
	ParentPostID string       `json:"parent_post_id,omitempty"`
	Seq          int64        `json:"seq"`
	BodyMD       string       `json:"body_md,omitempty"`
	BodyChars    int          `json:"body_chars"`
	Provenance   Provenance   `json:"provenance"`
	CreatedAt    string       `json:"created_at"`
	EditedAt     string       `json:"edited_at,omitempty"`
	Depth        int          `json:"depth"`
	ReplyCount   int          `json:"reply_count"`
	Snippet      string       `json:"snippet,omitempty"`
	Truncated    bool         `json:"truncated"`
	TopicTitle   string       `json:"topic_title,omitempty"`
}
type Options struct {
	NamespaceFilter string  `json:"-"`
	CategoryID      *string `json:"category_id,omitempty"`
	Archived        *bool   `json:"archived,omitempty"`
	Cursor          string  `json:"cursor,omitempty"`
	Limit           int     `json:"limit,omitempty"`
	SinceSeq        int64   `json:"since_seq,omitempty"`
	Status          string  `json:"status,omitempty"`
	Author          string  `json:"author,omitempty"`
	AuthorKind      string  `json:"author_kind,omitempty"`
	CreatedAfter    string  `json:"created_after,omitempty"`
	CreatedBefore   string  `json:"created_before,omitempty"`
	HasReplies      *bool   `json:"has_replies,omitempty"`
	WorkstreamID    string  `json:"workstream_id,omitempty"`
	ProjectID       string  `json:"project_id,omitempty"`
	Detail          string  `json:"detail,omitempty"`
	Fields          string  `json:"fields,omitempty"`
	MaxChars        int     `json:"max_chars,omitempty"`
}
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
	LastSeq    int64  `json:"last_seq,omitempty"`
	Next       string `json:"next"`
}
type Request struct {
	SortOrder             *int64          `json:"sort_order,omitempty"`
	Namespace             string          `json:"namespace,omitempty"`
	DestinationForumID    string          `json:"destination_forum_id,omitempty"`
	DestinationTopicID    string          `json:"destination_topic_id,omitempty"`
	DestinationCategoryID *string         `json:"destination_category_id,omitempty"`
	Description           *string         `json:"description,omitempty"`
	Subject               *string         `json:"subject,omitempty"`
	Attachments           *[]Attachment   `json:"attachments,omitempty"`
	Cascade               bool            `json:"cascade,omitempty"`
	PrincipalID           string          `json:"principal_id,omitempty"`
	PrincipalKind         string          `json:"principal_kind,omitempty"`
	Access                string          `json:"access,omitempty"`
	OwnerType             string          `json:"owner_type,omitempty"`
	OwnerID               string          `json:"owner_id,omitempty"`
	ForumID               string          `json:"forum_id,omitempty"`
	TopicID               string          `json:"topic_id,omitempty"`
	PostID                string          `json:"post_id,omitempty"`
	ID                    string          `json:"id,omitempty"`
	Title                 string          `json:"title,omitempty"`
	Config                json.RawMessage `json:"config_json,omitempty"`
	Metadata              json.RawMessage `json:"metadata_json,omitempty"`
	ParentPostID          string          `json:"parent_post_id,omitempty"`
	BodyMD                string          `json:"body_md,omitempty"`
	Query                 string          `json:"query,omitempty"`
	Options
}

// Store owns atomic sequences, parent validation, counts, FTS maintenance and ref writes.
// Post/ref writes must commit together. Lists use stable keyset cursors, not offsets.
type Store interface {
	CreateForum(context.Context, Forum) (Forum, error)
	GetForum(context.Context, string) (Forum, error)
	ListForums(context.Context, Caller, Options) (Page[Forum], error)
	CreateTopic(context.Context, Topic) (Topic, error)
	GetTopic(context.Context, string) (Topic, error)
	ListTopics(context.Context, string, Options) (Page[Topic], error)
	CreatePost(context.Context, Post) (Post, error)
	GetPost(context.Context, string) (Post, error)
	ListPosts(context.Context, string, Options) (Page[Post], error)
}
type Searcher interface {
	Search(context.Context, string, string, Options) (Page[Post], error)
}
type Service struct {
	Store    Store
	Searcher Searcher
	NewID    func() string
}

func New(s Store, search Searcher, newID func() string) *Service {
	return &Service{Store: s, Searcher: search, NewID: newID}
}
func CanAccess(f Forum, c Caller) bool {
	return c.ID != "" && (c.Operator || f.OwnerID == c.ID && f.OwnerType == c.Kind)
}
func (s *Service) Execute(ctx context.Context, c Caller, op string, r Request) (any, error) {
	if c.ID == "" || (c.Kind != "user" && c.Kind != "agent") {
		return nil, fmt.Errorf("%w: use an authenticated operator or agent session credential", ErrForbidden)
	}
	if err := validateOptions(&r.Options); err != nil {
		return nil, err
	}
	r.NamespaceFilter = r.Namespace
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	if isManagement(op) {
		return s.manage(ctx, c, op, r, now)
	}
	if op == "forum_create" {
		if err := validObjects(r); err != nil {
			return nil, err
		}
		if !c.Operator {
			return nil, fmt.Errorf("%w: only the operator can register forums; ask the owner to create one", ErrForbidden)
		}
		if err := ValidateNamespace(r.Namespace, r.ID); err != nil {
			return nil, err
		}
		if err := validTitle(r.Title); err != nil {
			return nil, err
		}
		for _, raw := range []json.RawMessage{r.Config, r.Metadata} {
			if len(raw) > 16384 || len(raw) > 0 && (!json.Valid(raw) || len(raw) == 0 || raw[0] != '{') {
				return nil, fmt.Errorf("%w: config_json and metadata_json must be JSON objects up to 16384 bytes", ErrInvalid)
			}
		}
		if len(r.Config) == 0 {
			r.Config = json.RawMessage(`{}`)
		}
		if len(r.Metadata) == 0 {
			r.Metadata = json.RawMessage(`{}`)
		}
		ownerType, ownerID := r.OwnerType, r.OwnerID
		if ownerType == "" && ownerID == "" {
			ownerType, ownerID = c.Kind, c.ID
		}
		if (ownerType != "user" && ownerType != "agent") || strings.TrimSpace(ownerID) == "" || len(ownerID) > 512 {
			return nil, fmt.Errorf("%w: owner_type must be user or agent and owner_id a principal", ErrInvalid)
		}
		if err := s.checkCategory(ctx, c, r.CategoryID, r.Namespace); err != nil {
			return nil, err
		}
		f, err := s.Store.CreateForum(ctx, Forum{SortOrder: sortValue(r.SortOrder), CategoryID: stringValue(r.CategoryID), Description: stringValue(r.Description), ID: r.ID, Namespace: r.Namespace, Title: r.Title, OwnerType: ownerType, OwnerID: ownerID, Policy: Policy{Mode: "owner-only"}, Config: r.Config, Metadata: r.Metadata, CreatedAt: now, UpdatedAt: now})
		return result("forum", f, "tether_forum_get", err)
	}
	if r.Status == "archived" {
		archived := true
		r.Archived = &archived
		r.Status = ""
	}
	if op == "forum_list" {
		p, err := s.Store.ListForums(ctx, c, r.Options)
		p.Next = "tether_forum_get"
		rows := make([]any, 0, len(p.Items))
		for _, v := range p.Items {
			rows = append(rows, forumSummary(v))
		}
		return project(map[string]any{"items": rows, "next_cursor": p.NextCursor, "has_more": p.HasMore, "next": p.Next}, r.Options), err
	}
	var f Forum
	var t Topic
	var p Post
	var err error
	switch op {
	case "topic_get", "post_create", "post_list":
		t, err = s.Store.GetTopic(ctx, r.TopicID)
		if err == nil {
			f, err = s.Store.GetForum(ctx, t.ForumID)
		}
	case "post_get":
		p, err = s.Store.GetPost(ctx, r.PostID)
		if err == nil {
			f, err = s.Store.GetForum(ctx, p.ForumID)
		}
	case "forum_get", "topic_create", "topic_list", "forum_search", "participant_list", "participant_add", "participant_revoke", "owner_set":
		f, err = s.Store.GetForum(ctx, r.ForumID)
	default:
		return nil, fmt.Errorf("%w: unknown forum operation", ErrInvalid)
	}
	if err != nil {
		return nil, err
	}
	management := op == "participant_list" || op == "participant_add" || op == "participant_revoke" || op == "owner_set"
	if management {
		if !c.Operator {
			return nil, fmt.Errorf("%w: only the operator manages participants and ownership", ErrForbidden)
		}
		st, ok := s.Store.(ParticipantStore)
		if !ok {
			return nil, fmt.Errorf("%w: host does not support participants", ErrInvalid)
		}
		principal := Principal{ID: r.PrincipalID, Kind: r.PrincipalKind}
		if op != "participant_list" && op != "owner_set" {
			if err := validPrincipal(principal); err != nil {
				return nil, err
			}
		}
		switch op {
		case "participant_list":
			pg, err := st.ListParticipants(ctx, f.ID, r.Options)
			return pg, err
		case "participant_add":
			if r.Access != "read" && r.Access != "post" {
				return nil, fmt.Errorf("%w: access must be read or post", ErrInvalid)
			}
			v := Participant{Principal: principal, Access: r.Access}
			return result("participant", v, "tether_forum_participant_list", st.SetParticipant(ctx, f.ID, v))
		case "participant_revoke":
			return result("revoked", principal, "tether_forum_participant_list", st.RevokeParticipant(ctx, f.ID, principal))
		case "owner_set":
			owner := Principal{ID: r.OwnerID, Kind: r.OwnerType}
			if owner.ID == "" && owner.Kind == "" {
				owner = c.Principal
			}
			if err := validPrincipal(owner); err != nil {
				return nil, err
			}
			v, err := st.SetOwner(ctx, f.ID, owner, now)
			return result("forum", v, "tether_forum_get", err)
		}
	}
	if !CanAccess(f, c) {
		st, ok := s.Store.(ParticipantStore)
		if !ok {
			return nil, ErrForbidden
		}
		grant, err := st.GetParticipant(ctx, f.ID, c.Principal)
		if errors.Is(err, ErrNotFound) {
			return nil, ErrForbidden
		}
		if err != nil {
			return nil, err
		}
		write := op == "topic_create" || op == "post_create"
		if grant.Access != "post" && (grant.Access != "read" || write) {
			return nil, ErrForbidden
		}
	}
	if op == "topic_create" || op == "post_create" {
		if err := s.writable(ctx, c, f); err != nil {
			return nil, err
		}
	}
	switch op {
	case "forum_get":
		var v any = forumSummary(f)
		if r.Detail == "full" {
			v = f
		}
		return project(map[string]any{"forum": v, "next": "tether_forum_topic_list; tether_forum_get detail=full for config"}, r.Options), nil
	case "topic_create":
		if err := validTitle(r.Title); err != nil {
			return nil, err
		}
		t, err = s.Store.CreateTopic(ctx, Topic{ID: s.NewID(), ForumID: f.ID, Title: r.Title, Status: "open", CreatedBy: c.Principal, CreatedAt: now, LastActivityAt: now})
		return result("topic", t, "tether_forum_post_create", err)
	case "topic_list":
		pg, e := s.Store.ListTopics(ctx, f.ID, r.Options)
		pg.Next = "tether_forum_topic_get"
		return project(pg, r.Options), e
	case "topic_get":
		pg, e := s.Store.ListPosts(ctx, t.ID, r.Options)
		compactPosts(&pg, r.Options, false)
		pg.Next = "tether_forum_post_get"
		return project(map[string]any{"topic": t, "outline": pg, "next": "tether_forum_post_get"}, r.Options), e
	case "post_create":
		if t.Archived || t.Status != "open" {
			return nil, fmt.Errorf("%w: topic is not open", ErrForbidden)
		}
		if strings.TrimSpace(r.BodyMD) == "" || len([]rune(r.BodyMD)) > 100000 {
			return nil, fmt.Errorf("%w: body_md must contain 1..100000 characters", ErrInvalid)
		}
		if err := validMessage(r); err != nil {
			return nil, err
		}
		p, err = s.Store.CreatePost(ctx, Post{Subject: stringValue(r.Subject), Attachments: attachmentValue(r.Attachments), ID: s.NewID(), TopicID: t.ID, ForumID: f.ID, ParentPostID: r.ParentPostID, BodyMD: r.BodyMD, BodyChars: len([]rune(r.BodyMD)), Provenance: c.Provenance, CreatedAt: now})
		pg := Page[Post]{Items: []Post{p}}
		compactPosts(&pg, Options{}, false)
		return result("post", pg.Items[0], "tether_forum_post_get", err)
	case "post_list":
		pg, e := s.Store.ListPosts(ctx, t.ID, r.Options)
		compactPosts(&pg, r.Options, false)
		pg.Next = "tether_forum_post_get"
		return project(pg, r.Options), e
	case "post_get":
		pg := Page[Post]{Items: []Post{p}}
		compactPosts(&pg, r.Options, true)
		return project(map[string]any{"post": pg.Items[0], "next": "tether_forum_post_get detail=full"}, r.Options), nil
	case "forum_search":
		if strings.TrimSpace(r.Query) == "" || len(r.Query) > 1000 {
			return nil, fmt.Errorf("%w: query required (up to 1000 bytes)", ErrInvalid)
		}
		pg, e := s.Searcher.Search(ctx, f.ID, r.Query, r.Options)
		compactPosts(&pg, r.Options, false)
		pg.Next = "tether_forum_post_get"
		return project(pg, r.Options), e
	}
	return nil, ErrInvalid
}
func result(key string, v any, next string, e error) (any, error) {
	return map[string]any{key: v, "next": next}, e
}
func validTitle(v string) error {
	if strings.TrimSpace(v) == "" || len([]rune(v)) > 300 {
		return fmt.Errorf("%w: title must contain 1..300 characters", ErrInvalid)
	}
	return nil
}
func validateOptions(o *Options) error {
	if o.Limit == 0 {
		o.Limit = 20
	}
	if o.Limit < 1 || o.Limit > 100 || o.SinceSeq < 0 || o.MaxChars < 0 || o.MaxChars > 100000 {
		return fmt.Errorf("%w: limit 1..100, since_seq >= 0, max_chars 0..100000", ErrInvalid)
	}
	if o.Detail == "" {
		o.Detail = "summary"
	}
	if o.Detail != "summary" && o.Detail != "preview" && o.Detail != "full" {
		return fmt.Errorf("%w: detail must be summary, preview or full", ErrInvalid)
	}
	if o.Status != "" && o.Status != "open" && o.Status != "resolved" && o.Status != "archived" {
		return fmt.Errorf("%w: unknown topic status", ErrInvalid)
	}
	if o.AuthorKind != "" && o.AuthorKind != "user" && o.AuthorKind != "agent" {
		return fmt.Errorf("%w: author_kind must be user or agent", ErrInvalid)
	}
	for _, value := range []*string{&o.CreatedAfter, &o.CreatedBefore} {
		if *value != "" {
			t, e := time.Parse(time.RFC3339Nano, *value)
			if e != nil {
				return fmt.Errorf("%w: date filters must be RFC3339", ErrInvalid)
			}
			*value = t.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
	}
	if o.CreatedAfter != "" && o.CreatedBefore != "" && o.CreatedAfter > o.CreatedBefore {
		return fmt.Errorf("%w: created_after must not exceed created_before", ErrInvalid)
	}
	return nil
}
func compactPosts(pg *Page[Post], o Options, get bool) {
	budget := o.MaxChars
	for i := range pg.Items {
		p := &pg.Items[i]
		r := []rune(p.BodyMD)
		if !get && p.Snippet != "" {
			r = []rune(p.Snippet)
		}
		n := 160
		if get && o.Detail == "preview" {
			n = 2000
		}
		if get && o.Detail == "full" {
			n = 100000
		}
		if o.MaxChars > 0 && n > budget {
			n = budget
		}
		if n > len(r) {
			n = len(r)
		}
		if o.MaxChars > 0 {
			budget -= n
		}
		p.Snippet = string(r[:min(160, n)])
		p.Truncated = n < p.BodyChars
		if get {
			p.BodyMD = string(r[:n])
			p.Snippet = ""
		} else {
			p.BodyMD = ""
		}
	}
}

// Projection affects records only; pagination and expansion hints always survive.
func project(v any, o Options) any {
	if o.Fields == "" {
		return v
	}
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	keep := map[string]bool{}
	for _, f := range strings.Split(o.Fields, ",") {
		keep[strings.TrimSpace(f)] = true
	}
	var walk func(any)
	walk = func(x any) {
		switch z := x.(type) {
		case []any:
			for _, a := range z {
				walk(a)
			}
		case map[string]any:
			if _, isRecord := z["id"]; isRecord {
				for k := range z {
					if !keep[k] && k != "id" {
						delete(z, k)
					}
				}
			} else {
				for _, a := range z {
					walk(a)
				}
			}
		}
	}
	walk(out)
	return out
}

func forumSummary(f Forum) map[string]any {
	return map[string]any{"sort_order": f.SortOrder, "category_id": f.CategoryID, "description": f.Description, "archived": f.Archived, "id": f.ID, "namespace": f.Namespace, "title": f.Title, "owner_type": f.OwnerType, "owner_id": f.OwnerID, "policy_json": f.Policy, "last_seq": f.LastSeq, "created_at": f.CreatedAt, "updated_at": f.UpdatedAt}
}

func validPrincipal(p Principal) error {
	if (p.Kind != "user" && p.Kind != "agent") || strings.TrimSpace(p.ID) == "" || p.ID != strings.TrimSpace(p.ID) || len(p.ID) > 512 {
		return fmt.Errorf("%w: principal kind must be user or agent with a nonempty stable id up to 512 bytes", ErrInvalid)
	}
	return nil
}
