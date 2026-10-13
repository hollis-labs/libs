package forums

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ManagementStore supplies atomic hierarchy/content mutations. The Service
// authorizes management before calling Mutate; hosts recheck structural and
// archive invariants inside the same transaction as content, counters, FTS and
// correlation refs. Clones preserve source attribution with explicit origin IDs.
// Empty-container deletes are safe by default; cascade is explicit.
type ManagementStore interface {
	GetCategory(context.Context, Caller, string) (Category, error)
	ListCategories(context.Context, Caller, Options) (Page[Category], error)
	Mutate(context.Context, string, Request, Caller, string, func() string) (any, error)
}

func isManagement(op string) bool {
	switch op {
	case "category_create", "category_get", "category_list", "category_update", "category_delete", "category_archive", "category_unarchive",
		"forum_update", "forum_delete", "forum_archive", "forum_unarchive", "forum_move", "forum_clone",
		"topic_update", "topic_delete", "topic_archive", "topic_unarchive", "topic_move", "topic_clone", "topic_fork",
		"post_update", "post_delete", "post_move", "post_clone":
		return true
	}
	return false
}
func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func attachmentValue(p *[]Attachment) []Attachment {
	if p == nil {
		return []Attachment{}
	}
	return *p
}
func sortValue(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func validObjects(r Request) error {
	if r.SortOrder != nil && (*r.SortOrder < -9007199254740991 || *r.SortOrder > 9007199254740991) {
		return fmt.Errorf("%w: sort_order must be a JSON-safe integer", ErrInvalid)
	}
	for _, v := range []json.RawMessage{r.Config, r.Metadata} {
		if len(v) > 0 && (len(v) > 16384 || !json.Valid(v) || strings.TrimSpace(string(v))[0] != '{') {
			return fmt.Errorf("%w: config_json and metadata_json must be objects up to 16384 bytes", ErrInvalid)
		}
	}
	if r.Description != nil && len([]rune(*r.Description)) > 10000 {
		return fmt.Errorf("%w: description exceeds 10000 characters", ErrInvalid)
	}
	return nil
}
func validMessage(r Request) error {
	if r.Subject != nil && len([]rune(*r.Subject)) > 300 {
		return fmt.Errorf("%w: subject exceeds 300 characters", ErrInvalid)
	}
	if r.Attachments != nil {
		if len(*r.Attachments) > 20 {
			return fmt.Errorf("%w: at most 20 attachments", ErrInvalid)
		}
		for _, a := range *r.Attachments {
			u, e := url.Parse(a.URI)
			if e != nil || u.Scheme == "" || len(a.URI) > 4096 || strings.TrimSpace(a.Name) == "" || len(a.Name) > 300 || len(a.MediaType) > 200 {
				return fmt.Errorf("%w: attachments require a name and absolute URI with bounded metadata", ErrInvalid)
			}
		}
	}
	return nil
}
func (s *Service) checkCategory(ctx context.Context, c Caller, id *string, ns string) error {
	if id == nil || *id == "" {
		return nil
	}
	st, ok := s.Store.(ManagementStore)
	if !ok {
		return ErrInvalid
	}
	cat, e := st.GetCategory(ctx, c, *id)
	if e != nil {
		return e
	}
	if cat.Namespace != ns {
		return fmt.Errorf("%w: category must belong to the same namespace", ErrInvalid)
	}
	if cat.Archived {
		return fmt.Errorf("%w: category is archived", ErrForbidden)
	}
	return nil
}
func (s *Service) writable(ctx context.Context, c Caller, f Forum) error {
	if f.Archived {
		return fmt.Errorf("%w: forum is archived", ErrForbidden)
	}
	if f.CategoryID != "" {
		return s.checkCategory(ctx, c, &f.CategoryID, f.Namespace)
	}
	return nil
}
func (s *Service) manage(ctx context.Context, c Caller, op string, r Request, now string) (any, error) {
	st, ok := s.Store.(ManagementStore)
	if !ok {
		return nil, fmt.Errorf("%w: host does not support hierarchy management", ErrInvalid)
	}
	if err := validObjects(r); err != nil {
		return nil, err
	}
	if strings.HasPrefix(op, "category_") {
		if op == "category_list" {
			p, e := st.ListCategories(ctx, c, r.Options)
			p.Next = "tether_forum_category_get"
			return project(p, r.Options), e
		}
		if op == "category_get" {
			v, e := st.GetCategory(ctx, c, stringValue(r.CategoryID))
			return result("category", v, "tether_forum_list", e)
		}
		if !c.Operator {
			return nil, ErrForbidden
		}
		if op == "category_create" {
			if err := ValidateNamespace(r.Namespace, r.ID); err != nil {
				return nil, err
			}
		}
		if op == "category_create" || op == "category_update" && r.Title != "" {
			if err := validTitle(r.Title); err != nil {
				return nil, err
			}
		}
		return st.Mutate(ctx, op, r, c, now, s.NewID)
	}
	var src Forum
	var err error
	switch {
	case strings.HasPrefix(op, "forum_"):
		src, err = s.Store.GetForum(ctx, r.ForumID)
	case strings.HasPrefix(op, "topic_"):
		var t Topic
		t, err = s.Store.GetTopic(ctx, r.TopicID)
		if err == nil {
			src, err = s.Store.GetForum(ctx, t.ForumID)
		}
	case strings.HasPrefix(op, "post_"):
		var p Post
		p, err = s.Store.GetPost(ctx, r.PostID)
		if err == nil {
			src, err = s.Store.GetForum(ctx, p.ForumID)
		}
	}
	if err != nil {
		return nil, err
	}
	// Moderation belongs to operators and forum owners, not ordinary participants.
	if !CanAccess(src, c) {
		return nil, ErrForbidden
	}
	if r.Title != "" {
		if err := validTitle(r.Title); err != nil {
			return nil, err
		}
	}
	if op == "forum_clone" {
		if err := ValidateNamespace(src.Namespace, r.ID); err != nil {
			return nil, err
		}
	}
	if op == "forum_move" && r.DestinationCategoryID == nil {
		return nil, fmt.Errorf("%w: destination_category_id required; empty means root", ErrInvalid)
	}
	if op == "topic_move" || op == "topic_clone" || op == "topic_fork" {
		if r.DestinationForumID == "" {
			return nil, fmt.Errorf("%w: destination_forum_id required", ErrInvalid)
		}
	}
	if op == "topic_fork" && r.PostID == "" {
		return nil, fmt.Errorf("%w: post_id selects the fork subtree", ErrInvalid)
	}
	if op == "post_move" || op == "post_clone" {
		if r.DestinationTopicID == "" {
			return nil, fmt.Errorf("%w: destination_topic_id required", ErrInvalid)
		}
	}
	if r.DestinationTopicID != "" {
		t, e := s.Store.GetTopic(ctx, r.DestinationTopicID)
		if e != nil {
			return nil, e
		}
		r.DestinationForumID = t.ForumID
	}
	if r.DestinationForumID != "" {
		dest, e := s.Store.GetForum(ctx, r.DestinationForumID)
		if e != nil {
			return nil, e
		}
		if !CanAccess(dest, c) {
			return nil, ErrForbidden
		}
		if dest.Namespace != src.Namespace {
			return nil, fmt.Errorf("%w: moves and clones stay within one namespace", ErrInvalid)
		}
	}
	if r.DestinationCategoryID != nil {
		if err := s.checkCategory(ctx, c, r.DestinationCategoryID, src.Namespace); err != nil {
			return nil, err
		}
	}
	if op == "post_update" {
		if r.BodyMD == "" && r.Subject == nil && r.Attachments == nil {
			return nil, fmt.Errorf("%w: supply body_md, subject or attachments", ErrInvalid)
		}
		if r.BodyMD != "" && (strings.TrimSpace(r.BodyMD) == "" || len([]rune(r.BodyMD)) > 100000) {
			return nil, fmt.Errorf("%w: body_md must contain 1..100000 characters", ErrInvalid)
		}
		if err := validMessage(r); err != nil {
			return nil, err
		}
	}
	out, err := st.Mutate(ctx, op, r, c, now, s.NewID)
	if err != nil {
		return nil, err
	}
	if row, ok := out.(map[string]any); ok {
		if p, ok := row["post"].(Post); ok {
			pg := Page[Post]{Items: []Post{p}}
			compactPosts(&pg, r.Options, false)
			row["post"] = pg.Items[0]
			row["next"] = "tether_forum_post_get"
		}
	}
	return project(out, r.Options), nil
}
