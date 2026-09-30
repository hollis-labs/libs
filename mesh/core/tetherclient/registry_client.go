package tether

// registry_client.go — typed client for the daemon's /registry/... federation
// directory routes. The types mirror the daemon's wire shape (they cannot be
// imported from the daemon, which keeps them internal); the method set is
// the read/write surface an agent or host needs, without the daemon-admin
// routes (bootstrap, reonboard).
//
// Errors are *APIError, as everywhere else in this package: a 404 is
// errors.Is(err, &APIError{StatusCode: 404}), a 400 carries code
// "invalid_request". Sync's two success shapes (204 for "no callback
// configured", 200 plus the refreshed profile) are reported as a third return
// value.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RegistryKind is the kind of a registry entry.
type RegistryKind string

// The registry kinds the daemon serves.
const (
	RegistryKindAgent   RegistryKind = "agent"
	RegistryKindProject RegistryKind = "project"
	RegistryKindGroup   RegistryKind = "group"
)

// RegistryStatus is the lifecycle state of a registry entry. A deregistered
// entry is StatusDeprecated: still readable by URN, left out of a default
// Search.
type RegistryStatus string

// The registry lifecycle states.
const (
	RegistryStatusActive     RegistryStatus = "active"
	RegistryStatusDeprecated RegistryStatus = "deprecated"
	RegistryStatusMerged     RegistryStatus = "merged"
	// RegistryStatusArchived marks an archived group: readable, but it accepts
	// no new messages.
	RegistryStatusArchived RegistryStatus = "archived"
)

// RegistryFieldClass says whether a profile field is authored intent or
// derived from something a machine can recreate.
type RegistryFieldClass string

// The field classes.
const (
	RegistryFieldClassDerived  RegistryFieldClass = "derived"
	RegistryFieldClassAuthored RegistryFieldClass = "authored"
)

// RegistryFieldMeta records who set a field, when, and how fresh it is.
type RegistryFieldMeta struct {
	Class         RegistryFieldClass `json:"class"`
	LastUpdatedBy string             `json:"last_updated_by,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at"`
	CachedAt      *time.Time         `json:"cached_at,omitempty"`
}

// RegistryExternalID ties one substrate-local identifier to a registry URN.
type RegistryExternalID struct {
	Substrate  string    `json:"substrate"`
	ExternalID string    `json:"external_id"`
	AttachedAt time.Time `json:"attached_at"`
}

// RegistrySkill is a learned skill: Name and LearnedAt are required, Via and
// Level optional.
type RegistrySkill struct {
	Name      string    `json:"name"`
	LearnedAt time.Time `json:"learned_at"`
	Via       string    `json:"via,omitempty"`
	Level     string    `json:"level,omitempty"`
}

// RegistryLink is a relationship to another entity; the kind vocabulary is
// open.
type RegistryLink struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// RegistryCallback names the endpoint the owning substrate exposes for Sync to
// refresh an entry from.
type RegistryCallback struct {
	Scheme string `json:"scheme"`
	Target string `json:"target"`
}

// RegistryProfile is a registry entry as the daemon serves it. On Register the
// server assigns URN, Kind, MuxInstanceID, CreatedAt and UpdatedAt.
type RegistryProfile struct {
	URN           string                       `json:"urn"`
	Kind          RegistryKind                 `json:"kind"`
	Owner         string                       `json:"owner,omitempty"`
	MuxInstanceID string                       `json:"mux_instance_id"`
	DisplayName   string                       `json:"display_name"`
	Title         string                       `json:"title,omitempty"`
	Role          string                       `json:"role,omitempty"`
	Description   string                       `json:"description,omitempty"`
	Avatar        string                       `json:"avatar,omitempty"`
	Project       string                       `json:"project,omitempty"`
	Status        RegistryStatus               `json:"status"`
	Callback      *RegistryCallback            `json:"callback,omitempty"`
	CachedAt      *time.Time                   `json:"cached_at,omitempty"`
	HealthStatus  string                       `json:"health_status,omitempty"`
	LastSeenAt    *time.Time                   `json:"last_seen_at,omitempty"`
	HostAddress   string                       `json:"host_address,omitempty"`
	MergedInto    string                       `json:"merged_into,omitempty"`
	KindMeta      json.RawMessage              `json:"kind_meta,omitempty"`
	LastUpdatedBy string                       `json:"last_updated_by,omitempty"`
	Tags          []string                     `json:"tags,omitempty"`
	Guidelines    string                       `json:"guidelines,omitempty"`
	EntryPoints   []string                     `json:"entry_points,omitempty"`
	Props         map[string]string            `json:"props,omitempty"`
	FieldMetadata map[string]RegistryFieldMeta `json:"field_metadata,omitempty"`
	ExternalIDs   []RegistryExternalID         `json:"external_ids,omitempty"`
	Capabilities  []string                     `json:"capabilities,omitempty"`
	Skills        []RegistrySkill              `json:"skills,omitempty"`
	Links         []RegistryLink               `json:"links,omitempty"`
	CreatedAt     time.Time                    `json:"created_at"`
	UpdatedAt     time.Time                    `json:"updated_at"`
}

// RegistryFilter narrows a Search; every field is optional and they combine
// with AND. An empty Status returns active entries only.
type RegistryFilter struct {
	Role       string
	Title      string
	Project    string
	Capability string
	SkillName  string
	Status     string
	Tag        string
}

// RegistryArrayMode says how an array patch merges into the stored list.
type RegistryArrayMode string

// The array patch modes.
const (
	RegistryArrayModeReplace RegistryArrayMode = "replace"
	RegistryArrayModeAppend  RegistryArrayMode = "append"
	RegistryArrayModeRemove  RegistryArrayMode = "remove"
)

// RegistryArrayPatch is the merge wrapper for an array field of an
// UpdateSelf patch. It marshals to {"mode": ..., "value": [...]}; decoding also
// accepts the bare-array shorthand, which means replace. Remove matches
// capabilities by string equality, skills by Name and links by (Kind, Target).
type RegistryArrayPatch[T any] struct {
	Mode  RegistryArrayMode `json:"mode"`
	Value []T               `json:"value"`
}

// UnmarshalJSON accepts both the explicit object and the bare-array shorthand.
func (p *RegistryArrayPatch[T]) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("tether: empty array patch")
	}
	if trimmed[0] == '[' {
		var arr []T
		if err := json.Unmarshal(data, &arr); err != nil {
			return fmt.Errorf("tether: array patch shorthand: %w", err)
		}
		p.Mode, p.Value = RegistryArrayModeReplace, arr
		return nil
	}
	var obj struct {
		Mode  RegistryArrayMode `json:"mode"`
		Value []T               `json:"value"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("tether: array patch object: %w", err)
	}
	switch obj.Mode {
	case RegistryArrayModeReplace, RegistryArrayModeAppend, RegistryArrayModeRemove:
	default:
		return fmt.Errorf("tether: invalid array patch mode %q", obj.Mode)
	}
	p.Mode, p.Value = obj.Mode, obj.Value
	return nil
}

// RegistryUpdatePatch is the partial update UpdateSelf sends. A nil scalar
// pointer leaves the field alone; a pointer to "" clears it. A nil array patch
// leaves the list alone. KindMeta replaces on presence. LastUpdatedBy is
// required: the daemon answers 400 without it.
type RegistryUpdatePatch struct {
	Owner         *string                            `json:"owner,omitempty"`
	DisplayName   *string                            `json:"display_name,omitempty"`
	Title         *string                            `json:"title,omitempty"`
	Role          *string                            `json:"role,omitempty"`
	Description   *string                            `json:"description,omitempty"`
	Avatar        *string                            `json:"avatar,omitempty"`
	Project       *string                            `json:"project,omitempty"`
	Status        *RegistryStatus                    `json:"status,omitempty"`
	HealthStatus  *string                            `json:"health_status,omitempty"`
	LastSeenAt    *time.Time                         `json:"last_seen_at,omitempty"`
	HostAddress   *string                            `json:"host_address,omitempty"`
	KindMeta      json.RawMessage                    `json:"kind_meta,omitempty"`
	LastUpdatedBy string                             `json:"last_updated_by"`
	Tags          *RegistryArrayPatch[string]        `json:"tags,omitempty"`
	Guidelines    *string                            `json:"guidelines,omitempty"`
	EntryPoints   *RegistryArrayPatch[string]        `json:"entry_points,omitempty"`
	Props         map[string]string                  `json:"props,omitempty"`
	FieldMetadata map[string]RegistryFieldMeta       `json:"field_metadata,omitempty"`
	Capabilities  *RegistryArrayPatch[string]        `json:"capabilities,omitempty"`
	Skills        *RegistryArrayPatch[RegistrySkill] `json:"skills,omitempty"`
	Links         *RegistryArrayPatch[RegistryLink]  `json:"links,omitempty"`
}

// RegistryClient is the typed handle for the daemon's /registry tree. Get one
// from Client.Registry.
type RegistryClient struct {
	c *Client
}

// Registry returns a RegistryClient bound to c. It is cheap; call it inline.
func (c *Client) Registry() *RegistryClient {
	return &RegistryClient{c: c}
}

// Register POSTs p under /registry/{kind}s and returns the canonical profile.
// The server mints the URN and rejects a caller-supplied one with a 400; it
// also assigns Kind, MuxInstanceID, CreatedAt and UpdatedAt.
func (rc *RegistryClient) Register(ctx context.Context, kind RegistryKind, p RegistryProfile) (RegistryProfile, error) {
	seg, err := registryPluralSegment(kind)
	if err != nil {
		return RegistryProfile{}, err
	}
	var out RegistryProfile
	if _, err := rc.c.roundTrip(ctx, http.MethodPost, "/registry/"+seg, p, []int{http.StatusCreated}, &out, readAPIError); err != nil {
		return RegistryProfile{}, err
	}
	return out, nil
}

// Lookup GETs one profile by URN. The kind is derived from the URN's prefix.
func (rc *RegistryClient) Lookup(ctx context.Context, urn string) (RegistryProfile, error) {
	return rc.LookupWithInclude(ctx, urn)
}

// LookupWithInclude is Lookup with the sensitive fields named in include
// added to the answer ("external_ids", "callback", "host_address",
// "kind_meta", or "all").
func (rc *RegistryClient) LookupWithInclude(ctx context.Context, urn string, include ...string) (RegistryProfile, error) {
	seg, err := registryKindSegmentFromURN(urn)
	if err != nil {
		return RegistryProfile{}, err
	}
	path := "/registry/" + seg + "/" + url.PathEscape(urn)
	if len(include) > 0 {
		path += "?include=" + url.QueryEscape(strings.Join(include, ","))
	}
	var out RegistryProfile
	if _, err := rc.c.roundTrip(ctx, http.MethodGet, path, nil, []int{http.StatusOK}, &out, readAPIError); err != nil {
		return RegistryProfile{}, err
	}
	return out, nil
}

// LookupBy resolves a substrate-local identifier to one profile. substrate may
// be empty.
func (rc *RegistryClient) LookupBy(ctx context.Context, kind RegistryKind, externalID, substrate string) (RegistryProfile, error) {
	return rc.LookupByWithInclude(ctx, kind, externalID, substrate)
}

// LookupByWithInclude is LookupBy with extra fields included, as in
// LookupWithInclude. Including exactly "all" asks for the unredacted profile.
//
// The daemon wraps the answer in an object keyed by the SINGULAR kind
// ({"agent": {...}}), where Search keys on the plural segment.
func (rc *RegistryClient) LookupByWithInclude(ctx context.Context, kind RegistryKind, externalID, substrate string, include ...string) (RegistryProfile, error) {
	seg, err := registryPluralSegment(kind)
	if err != nil {
		return RegistryProfile{}, err
	}
	q := url.Values{}
	q.Set("external_id", externalID)
	if substrate != "" {
		q.Set("substrate", substrate)
	}
	if len(include) == 1 && include[0] == "all" {
		q.Set("full", "true")
	} else if len(include) > 0 {
		q.Set("include", strings.Join(include, ","))
	}
	var env map[string]json.RawMessage
	if _, err := rc.c.roundTrip(ctx, http.MethodGet, withQuery("/registry/"+seg, q), nil, []int{http.StatusOK}, &env, readAPIError); err != nil {
		return RegistryProfile{}, err
	}
	raw, ok := env[string(kind)]
	if !ok {
		return RegistryProfile{}, fmt.Errorf("decode lookup-by envelope: missing key %q", kind)
	}
	var out RegistryProfile
	if err := json.Unmarshal(raw, &out); err != nil {
		return RegistryProfile{}, fmt.Errorf("decode lookup-by profile: %w", err)
	}
	return out, nil
}

// Search lists the profiles of kind that match f. It returns a non-nil empty
// slice when nothing matches. The daemon keys the list on the PLURAL segment
// ({"agents": [...]}).
func (rc *RegistryClient) Search(ctx context.Context, kind RegistryKind, f RegistryFilter) ([]RegistryProfile, error) {
	seg, err := registryPluralSegment(kind)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	for k, v := range map[string]string{
		"role": f.Role, "title": f.Title, "project": f.Project, "capability": f.Capability,
		"skill_name": f.SkillName, "status": f.Status, "tag": f.Tag,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var env map[string]json.RawMessage
	if _, err := rc.c.roundTrip(ctx, http.MethodGet, withQuery("/registry/"+seg, q), nil, []int{http.StatusOK}, &env, readAPIError); err != nil {
		return nil, err
	}
	raw, ok := env[seg]
	if !ok {
		return []RegistryProfile{}, nil
	}
	var out []RegistryProfile
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode search list: %w", err)
	}
	if out == nil {
		out = []RegistryProfile{}
	}
	return out, nil
}

// UpdateSelf PATCHes the entry with a partial-merge patch and returns the
// updated profile. patch.LastUpdatedBy is required.
func (rc *RegistryClient) UpdateSelf(ctx context.Context, urn string, patch RegistryUpdatePatch) (RegistryProfile, error) {
	seg, err := registryKindSegmentFromURN(urn)
	if err != nil {
		return RegistryProfile{}, err
	}
	var out RegistryProfile
	if _, err := rc.c.roundTrip(ctx, http.MethodPatch, "/registry/"+seg+"/"+url.PathEscape(urn), patch, []int{http.StatusOK}, &out, readAPIError); err != nil {
		return RegistryProfile{}, err
	}
	return out, nil
}

// Deregister soft-deletes the entry and returns it with status "deprecated".
func (rc *RegistryClient) Deregister(ctx context.Context, urn string) (RegistryProfile, error) {
	seg, err := registryKindSegmentFromURN(urn)
	if err != nil {
		return RegistryProfile{}, err
	}
	var out RegistryProfile
	if _, err := rc.c.roundTrip(ctx, http.MethodDelete, "/registry/"+seg+"/"+url.PathEscape(urn), nil, []int{http.StatusOK}, &out, readAPIError); err != nil {
		return RegistryProfile{}, err
	}
	return out, nil
}

// Merge folds urnSrc into urnDst and returns the destination profile.
func (rc *RegistryClient) Merge(ctx context.Context, urnSrc, urnDst string) (RegistryProfile, error) {
	seg, err := registryKindSegmentFromURN(urnSrc)
	if err != nil {
		return RegistryProfile{}, err
	}
	var out RegistryProfile
	path := "/registry/" + seg + "/" + url.PathEscape(urnSrc) + "/merge"
	if _, err := rc.c.roundTrip(ctx, http.MethodPost, path, map[string]string{"into": urnDst}, []int{http.StatusOK}, &out, readAPIError); err != nil {
		return RegistryProfile{}, err
	}
	return out, nil
}

// Sync asks the daemon to refresh the entry from its callback. It returns
// synced=false and a zero profile when the entry has no callback (the daemon
// answers 204), and synced=true with the refreshed profile otherwise (200).
func (rc *RegistryClient) Sync(ctx context.Context, urn string) (profile RegistryProfile, synced bool, err error) {
	seg, err := registryKindSegmentFromURN(urn)
	if err != nil {
		return RegistryProfile{}, false, err
	}
	var out RegistryProfile
	status, err := rc.c.roundTrip(ctx, http.MethodPost, "/registry/"+seg+"/"+url.PathEscape(urn)+"/sync", nil,
		[]int{http.StatusOK, http.StatusNoContent}, &out, readAPIError)
	if err != nil {
		return RegistryProfile{}, false, err
	}
	if status == http.StatusNoContent {
		return RegistryProfile{}, false, nil
	}
	return out, true, nil
}

// registryPluralSegment translates a kind to its URL segment.
func registryPluralSegment(k RegistryKind) (string, error) {
	switch k {
	case RegistryKindAgent:
		return "agents", nil
	case RegistryKindProject:
		return "projects", nil
	case RegistryKindGroup:
		return "groups", nil
	default:
		return "", fmt.Errorf("tether: unsupported registry kind %q", string(k))
	}
}

// registryKindSegmentFromURN derives the URL segment from the URN's prefix:
// agt_ is an agent, prj_ a project. It deliberately does not know grp_: groups
// are served from /groups (see GroupsClient), not /registry/groups.
func registryKindSegmentFromURN(urn string) (string, error) {
	const prefix = "msg://agent/agent-mux/"
	tail := strings.TrimPrefix(urn, prefix)
	switch {
	case strings.HasPrefix(tail, "agt_"):
		return "agents", nil
	case strings.HasPrefix(tail, "prj_"):
		return "projects", nil
	default:
		return "", fmt.Errorf("tether: cannot infer registry kind from URN %q", urn)
	}
}
