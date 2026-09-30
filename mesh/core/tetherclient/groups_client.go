package tether

// groups_client.go — typed client for the daemon's group-messaging routes
// (/groups and /mentions). A group is a registry entry of kind "group" with
// members, per-member read cursors and a sequenced message log; the routes
// take the caller's member URN per request (`as`/`by`/`from`) rather than
// deriving it, which is the daemon's same-host identity model.
//
// Errors are *APIError (404 not_found, 400 invalid_request, 403 forbidden,
// 423 locked for an archived group), except a 400 that names candidates for an
// ambiguous @-mention, which is a *GroupAmbiguousMentionError.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// RegistryMemberRole is a member's role inside a group.
type RegistryMemberRole string

// The group member roles. The owner is the creator; a moderator can invite
// and remove; a member can post and read.
const (
	RegistryMemberRoleMember    RegistryMemberRole = "member"
	RegistryMemberRoleModerator RegistryMemberRole = "moderator"
	RegistryMemberRoleOwner     RegistryMemberRole = "owner"
)

// GroupMember is one membership row.
type GroupMember struct {
	GroupURN    string             `json:"group_urn"`
	MemberURN   string             `json:"member_urn"`
	Role        RegistryMemberRole `json:"role"`
	JoinedAt    time.Time          `json:"joined_at"`
	LastReadSeq int64              `json:"last_read_seq"`
	// DisplayName is filled by ListMembers from the registry.
	DisplayName string `json:"display_name,omitempty"`
}

// GroupMessage is one message in a group's sequenced log.
type GroupMessage struct {
	ID          string          `json:"id"`
	GroupURN    string          `json:"group_urn"`
	GroupSeq    int64           `json:"group_seq"`
	FromURN     string          `json:"from_urn"`
	Kind        string          `json:"kind"`
	ThreadID    string          `json:"thread_id,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	ContentType string          `json:"content_type,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	// FanoutError is set only on the message a send returns, and only when
	// durable per-recipient delivery was attempted and failed; the room post
	// itself still succeeded. Empty does not guarantee delivery.
	FanoutError string `json:"fanout_error,omitempty"`
}

// GroupAmbiguousMentionError is the daemon's answer to a send whose short
// @-token matches more than one registry entry. Re-send with the full URN of
// one of Candidates. It matches errors.Is(err, &APIError{StatusCode: 400}).
type GroupAmbiguousMentionError struct {
	Token      string
	Candidates []string
}

func (e *GroupAmbiguousMentionError) Error() string {
	return "tether 400 (invalid_request): ambiguous mention @" + e.Token + " matches " + strconv.Itoa(len(e.Candidates)) + " entries"
}

// Is reports a match against a 400 invalid_request *APIError, which is the
// status and code the daemon sends it under.
func (e *GroupAmbiguousMentionError) Is(target error) bool {
	t, ok := target.(*APIError)
	if !ok {
		return false
	}
	return (t.StatusCode == 0 || t.StatusCode == http.StatusBadRequest) && (t.Code == "" || t.Code == "invalid_request")
}

// CreateGroupRequest is the input of Groups().Create. CreatorURN is recorded as
// the profile's LastUpdatedBy.
type CreateGroupRequest struct {
	DisplayName  string
	Description  string
	Role         string   // the group's free-form category
	Capabilities []string // topic tags
	CreatorURN   string
}

// SendGroupRequest is the input of Groups().Send.
type SendGroupRequest struct {
	From        string
	Kind        string // the daemon defaults it to "message"
	ThreadID    string
	ContentType string
	Payload     json.RawMessage
}

// SendGroupResult is the daemon's acknowledgement of a send.
type SendGroupResult struct {
	MessageID string `json:"message_id"`
	GroupSeq  int64  `json:"group_seq"`
	// FanoutError is non-empty only when per-recipient delivery was attempted
	// and failed; the room post itself always succeeds.
	FanoutError string `json:"fanout_error,omitempty"`
}

// ListMessagesParams are the filters of ListMessages. As, the caller's member
// URN, is required.
type ListMessagesParams struct {
	As       string
	SinceSeq int64
	ThreadID string
	Limit    int
}

// ListGroupMessagesResult is one page of a group's messages. NextSeq is where
// the next page starts.
type ListGroupMessagesResult struct {
	Messages []GroupMessage `json:"messages"`
	NextSeq  int64          `json:"next_seq"`
}

// MentionsParams are the filters of Mentions. As, the member URN whose feed is
// read, is required.
type MentionsParams struct {
	As    string
	Since time.Time
	Limit int
}

// GroupsClient is the typed handle for the daemon's /groups and /mentions
// routes. Get one from Client.Groups.
type GroupsClient struct {
	c *Client
}

// Groups returns a GroupsClient bound to c. It is cheap; call it inline.
func (c *Client) Groups() *GroupsClient {
	return &GroupsClient{c: c}
}

func (gc *GroupsClient) do(ctx context.Context, method, path string, body any, ok int, out any) error {
	_, err := gc.c.roundTrip(ctx, method, path, body, []int{ok}, out, readGroupError)
	return err
}

func groupPath(urn string, tail ...string) string {
	p := "/groups/" + url.PathEscape(urn)
	for _, t := range tail {
		p += "/" + t
	}
	return p
}

// Create POSTs /groups and returns the new group's profile (kind "group", URN
// minted by the daemon).
func (gc *GroupsClient) Create(ctx context.Context, req CreateGroupRequest) (RegistryProfile, error) {
	p := RegistryProfile{
		DisplayName:   req.DisplayName,
		Description:   req.Description,
		Role:          req.Role,
		Capabilities:  req.Capabilities,
		LastUpdatedBy: req.CreatorURN,
	}
	var out RegistryProfile
	err := gc.do(ctx, http.MethodPost, "/groups", p, http.StatusCreated, &out)
	return out, err
}

// Lookup GETs /groups/{urn}. A URN that is not a group is a 404.
func (gc *GroupsClient) Lookup(ctx context.Context, urn string) (RegistryProfile, error) {
	if urn == "" {
		return RegistryProfile{}, errEmptyArg("urn")
	}
	var out RegistryProfile
	err := gc.do(ctx, http.MethodGet, groupPath(urn), nil, http.StatusOK, &out)
	return out, err
}

// ListForMember lists the groups memberURN belongs to, as a non-nil slice.
func (gc *GroupsClient) ListForMember(ctx context.Context, memberURN string) ([]RegistryProfile, error) {
	if memberURN == "" {
		return nil, errEmptyArg("memberURN")
	}
	var env struct {
		Groups []RegistryProfile `json:"groups"`
	}
	if err := gc.do(ctx, http.MethodGet, "/groups?member="+url.QueryEscape(memberURN), nil, http.StatusOK, &env); err != nil {
		return nil, err
	}
	if env.Groups == nil {
		env.Groups = []RegistryProfile{}
	}
	return env.Groups, nil
}

// Archive archives the group on behalf of byURN and returns it.
func (gc *GroupsClient) Archive(ctx context.Context, grpURN, byURN string) (RegistryProfile, error) {
	if grpURN == "" {
		return RegistryProfile{}, errEmptyArg("grpURN")
	}
	var out RegistryProfile
	err := gc.do(ctx, http.MethodDelete, groupPath(grpURN)+"?as="+url.QueryEscape(byURN), nil, http.StatusOK, &out)
	return out, err
}

// AddMember adds memberURN on behalf of byURN. An empty role means "member".
func (gc *GroupsClient) AddMember(ctx context.Context, grpURN, memberURN, byURN string, role RegistryMemberRole) (GroupMember, error) {
	if grpURN == "" {
		return GroupMember{}, errEmptyArg("grpURN")
	}
	if memberURN == "" {
		return GroupMember{}, errEmptyArg("memberURN")
	}
	var out GroupMember
	err := gc.do(ctx, http.MethodPost, groupPath(grpURN, "members"),
		map[string]string{"member": memberURN, "by": byURN, "role": string(role)}, http.StatusCreated, &out)
	return out, err
}

// RemoveMember removes memberURN on behalf of byURN.
func (gc *GroupsClient) RemoveMember(ctx context.Context, grpURN, memberURN, byURN string) error {
	if grpURN == "" {
		return errEmptyArg("grpURN")
	}
	if memberURN == "" {
		return errEmptyArg("memberURN")
	}
	return gc.do(ctx, http.MethodDelete, groupPath(grpURN, "members", url.PathEscape(memberURN))+"?as="+url.QueryEscape(byURN), nil, http.StatusOK, nil)
}

// Leave removes memberURN from the group at its own request.
func (gc *GroupsClient) Leave(ctx context.Context, grpURN, memberURN string) error {
	if grpURN == "" {
		return errEmptyArg("grpURN")
	}
	if memberURN == "" {
		return errEmptyArg("memberURN")
	}
	return gc.do(ctx, http.MethodPost, groupPath(grpURN, "leave"), map[string]string{"member": memberURN}, http.StatusOK, nil)
}

// SetMemberRole changes a member's role on behalf of byURN.
func (gc *GroupsClient) SetMemberRole(ctx context.Context, grpURN, memberURN string, role RegistryMemberRole, byURN string) error {
	if grpURN == "" {
		return errEmptyArg("grpURN")
	}
	if memberURN == "" {
		return errEmptyArg("memberURN")
	}
	return gc.do(ctx, http.MethodPatch, groupPath(grpURN, "members", url.PathEscape(memberURN)),
		map[string]string{"role": string(role), "by": byURN}, http.StatusOK, nil)
}

// ListMembers lists the group's members, as a non-nil slice.
func (gc *GroupsClient) ListMembers(ctx context.Context, grpURN string) ([]GroupMember, error) {
	if grpURN == "" {
		return nil, errEmptyArg("grpURN")
	}
	var env struct {
		Members []GroupMember `json:"members"`
	}
	if err := gc.do(ctx, http.MethodGet, groupPath(grpURN, "members"), nil, http.StatusOK, &env); err != nil {
		return nil, err
	}
	if env.Members == nil {
		env.Members = []GroupMember{}
	}
	return env.Members, nil
}

// Send posts a message to the group. A send whose @-mention is ambiguous fails
// with a *GroupAmbiguousMentionError; an archived group answers 423.
func (gc *GroupsClient) Send(ctx context.Context, grpURN string, req SendGroupRequest) (SendGroupResult, error) {
	if grpURN == "" {
		return SendGroupResult{}, errEmptyArg("grpURN")
	}
	var out SendGroupResult
	err := gc.do(ctx, http.MethodPost, groupPath(grpURN, "messages"), map[string]any{
		"from":         req.From,
		"kind":         req.Kind,
		"thread_id":    req.ThreadID,
		"content_type": req.ContentType,
		"payload":      req.Payload,
	}, http.StatusCreated, &out)
	return out, err
}

// ListMessages reads a page of the group's messages. It does not move the read
// cursor; MarkRead does.
func (gc *GroupsClient) ListMessages(ctx context.Context, grpURN string, p ListMessagesParams) (ListGroupMessagesResult, error) {
	if grpURN == "" {
		return ListGroupMessagesResult{}, errEmptyArg("grpURN")
	}
	q := url.Values{}
	q.Set("as", p.As)
	if p.SinceSeq != 0 {
		q.Set("since_seq", strconv.FormatInt(p.SinceSeq, 10))
	}
	if p.ThreadID != "" {
		q.Set("thread_id", p.ThreadID)
	}
	if p.Limit != 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	var out ListGroupMessagesResult
	if err := gc.do(ctx, http.MethodGet, withQuery(groupPath(grpURN, "messages"), q), nil, http.StatusOK, &out); err != nil {
		return ListGroupMessagesResult{}, err
	}
	if out.Messages == nil {
		out.Messages = []GroupMessage{}
	}
	return out, nil
}

// MarkRead advances memberURN's read cursor to upToSeq. It is monotonic: a
// smaller value than the current cursor changes nothing.
func (gc *GroupsClient) MarkRead(ctx context.Context, grpURN, memberURN string, upToSeq int64) error {
	if grpURN == "" {
		return errEmptyArg("grpURN")
	}
	if memberURN == "" {
		return errEmptyArg("memberURN")
	}
	return gc.do(ctx, http.MethodPost, groupPath(grpURN, "read"),
		map[string]any{"up_to_seq": upToSeq, "as": memberURN}, http.StatusOK, nil)
}

// Mentions lists the group messages that @-mention p.As, from its personal
// inbox, as a non-nil slice.
func (gc *GroupsClient) Mentions(ctx context.Context, p MentionsParams) ([]GroupMessage, error) {
	q := url.Values{}
	q.Set("as", p.As)
	if !p.Since.IsZero() {
		q.Set("since", p.Since.UTC().Format(time.RFC3339))
	}
	if p.Limit != 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	var env struct {
		Mentions []GroupMessage `json:"mentions"`
	}
	if err := gc.do(ctx, http.MethodGet, withQuery("/mentions", q), nil, http.StatusOK, &env); err != nil {
		return nil, err
	}
	if env.Mentions == nil {
		env.Mentions = []GroupMessage{}
	}
	return env.Mentions, nil
}

// readGroupError is readAPIError plus the ambiguous-mention shape: a 400 that
// carries a candidate list becomes a *GroupAmbiguousMentionError.
func readGroupError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusBadRequest {
		var amb struct {
			Token      string   `json:"token"`
			Candidates []string `json:"candidates"`
		}
		if json.Unmarshal(body, &amb) == nil && len(amb.Candidates) > 0 {
			return &GroupAmbiguousMentionError{Token: amb.Token, Candidates: amb.Candidates}
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return readAPIError(resp)
}
