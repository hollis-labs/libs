package capability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/internal/strictjson"
)

// ContractVersion versions the explicit grant contract, independently of the
// subprocess protocol and each capability descriptor's schema version.
const ContractVersion = 1

// MaxSafeInteger is the maximum generation representable exactly by Go and JS.
const MaxSafeInteger uint64 = 9007199254740991

// RuntimeIdentity is the host-issued incarnation tuple. It is distinct from a
// manifest's server.runtime execution engine and from a verified caller identity.
type RuntimeIdentity struct {
	HostInstance    string `json:"host_instance"`
	OwnerID         string `json:"owner_id"`
	OwnerGeneration uint64 `json:"owner_generation"`
}

// Grant is discovery data, not proof of permission at execution time. The host
// normalizes Scope under its descriptor and enforces current policy separately.
// Identifiers and timestamps are preserved without normalization.
type Grant struct {
	GrantID         string          `json:"grant_id"`
	Name            string          `json:"name"`
	SchemaVersion   uint32          `json:"schema_version"`
	Scope           json.RawMessage `json:"scope"`
	HostInstance    string          `json:"host_instance"`
	OwnerID         string          `json:"owner_id"`
	OwnerGeneration uint64          `json:"owner_generation"`
	Audience        string          `json:"audience"`
	IssuedAt        string          `json:"issued_at"`
	ExpiresAt       string          `json:"expires_at"`
	PolicyRevision  string          `json:"policy_revision"`
}

// GrantSet encodes nil and empty sets as [], and refuses null on decode.
type GrantSet []Grant

// GrantValidationError classifies a structural failure without including the
// rejected value, which may contain sensitive scope or identity data.
type GrantValidationError struct{ Field, Reason string }

func (e *GrantValidationError) Error() string { return "capability: " + e.Field + ": " + e.Reason }
func invalid(field, reason string) error      { return &GrantValidationError{Field: field, Reason: reason} }

func (r RuntimeIdentity) Validate() error {
	for _, f := range []struct{ name, value string }{{"host_instance", r.HostInstance}, {"owner_id", r.OwnerID}} {
		if !utf8.ValidString(f.value) || strings.TrimSpace(f.value) == "" {
			return invalid(f.name, "required nonblank string")
		}
	}
	if r.OwnerGeneration == 0 || r.OwnerGeneration > MaxSafeInteger {
		return invalid("owner_generation", "required positive safe integer")
	}
	return nil
}

var utcTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)

func timestamp(field, value string) (time.Time, error) {
	if !utcTimestamp.MatchString(value) {
		return time.Time{}, invalid(field, "required UTC RFC3339 timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, invalid(field, "invalid UTC timestamp")
	}
	return t, nil
}

// Validate checks shape, tuple, JSON syntax and timestamp ordering only. It does
// not check current expiry, descriptor support, audience policy or authorization.
func (g Grant) Validate() error {
	if err := (RuntimeIdentity{g.HostInstance, g.OwnerID, g.OwnerGeneration}).Validate(); err != nil {
		return err
	}
	for _, f := range []struct{ name, value string }{{"grant_id", g.GrantID}, {"name", g.Name}, {"audience", g.Audience}, {"policy_revision", g.PolicyRevision}} {
		if !utf8.ValidString(f.value) || strings.TrimSpace(f.value) == "" {
			return invalid(f.name, "required nonblank string")
		}
	}
	if g.SchemaVersion == 0 {
		return invalid("schema_version", "required positive uint32")
	}
	if bytes.Equal(bytes.TrimSpace(g.Scope), []byte("null")) || strictjson.ValidatePortable(g.Scope) != nil {
		return invalid("scope", "required non-null JSON without duplicate keys")
	}
	issued, err := timestamp("issued_at", g.IssuedAt)
	if err != nil {
		return err
	}
	expires, err := timestamp("expires_at", g.ExpiresAt)
	if err != nil {
		return err
	}
	if !expires.After(issued) {
		return invalid("expires_at", "must be after issued_at")
	}
	return nil
}

func (s GrantSet) Validate() error {
	seen := make(map[string]bool, len(s))
	for i, g := range s {
		if err := g.Validate(); err != nil {
			return invalid(fmt.Sprintf("grants[%d]", i), err.Error())
		}
		if seen[g.GrantID] {
			return invalid(fmt.Sprintf("grants[%d].grant_id", i), "duplicate grant ID")
		}
		seen[g.GrantID] = true
	}
	return nil
}

// ValidateForRuntime checks the single canonical tuple, including an empty set.
func (s GrantSet) ValidateForRuntime(r RuntimeIdentity) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	for i, g := range s {
		if g.HostInstance != r.HostInstance || g.OwnerID != r.OwnerID || g.OwnerGeneration != r.OwnerGeneration {
			return invalid(fmt.Sprintf("grants[%d]", i), "runtime tuple mismatch")
		}
	}
	return nil
}

// rawInteger rejects fraction/exponent spellings before any numeric conversion.
func rawInteger(v json.RawMessage) bool {
	b := bytes.TrimSpace(v)
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (r RuntimeIdentity) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type plain RuntimeIdentity
	return json.Marshal(plain(r))
}
func (r *RuntimeIdentity) UnmarshalJSON(data []byte) error {
	f, err := strictjson.Object(data, "host_instance", "owner_id", "owner_generation")
	if err != nil {
		return invalid("runtime", "invalid closed JSON object")
	}
	if !rawInteger(f["owner_generation"]) {
		return invalid("owner_generation", "required decimal integer token")
	}
	type plain RuntimeIdentity
	var next plain
	if json.Unmarshal(data, &next) != nil {
		return invalid("runtime", "invalid field type or range")
	}
	if err := RuntimeIdentity(next).Validate(); err != nil {
		return err
	}
	*r = RuntimeIdentity(next)
	return nil
}
func (g Grant) MarshalJSON() ([]byte, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	type plain Grant
	return json.Marshal(plain(g))
}
func (g *Grant) UnmarshalJSON(data []byte) error {
	f, err := strictjson.Object(data, "grant_id", "name", "schema_version", "scope", "host_instance", "owner_id", "owner_generation", "audience", "issued_at", "expires_at", "policy_revision")
	if err != nil {
		return invalid("grant", "invalid closed JSON object")
	}
	for _, k := range []string{"schema_version", "owner_generation"} {
		if !rawInteger(f[k]) {
			return invalid(k, "required decimal integer token")
		}
	}
	type plain Grant
	var next plain
	if json.Unmarshal(data, &next) != nil {
		return invalid("grant", "invalid field type or range")
	}
	if err := Grant(next).Validate(); err != nil {
		return err
	}
	*g = Grant(next)
	return nil
}
func (s GrantSet) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]Grant(s))
}
func (s *GrantSet) UnmarshalJSON(data []byte) error {
	b := bytes.TrimSpace(data)
	if len(b) == 0 || b[0] != '[' || strictjson.Validate(data) != nil {
		return invalid("grants", "required JSON array without duplicate keys")
	}
	var next []Grant
	if err := json.Unmarshal(data, &next); err != nil {
		return invalid("grants", "invalid grant array")
	}
	if err := GrantSet(next).Validate(); err != nil {
		return err
	}
	*s = GrantSet(next)
	return nil
}
