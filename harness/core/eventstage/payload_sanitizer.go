package eventstage

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
	"github.com/hollis-labs/libs/security/redact"
)

// NewPayloadSanitizer constructs an immutable, concurrently usable adapter for
// the shared redaction engine. Policy selection is exact kind, longest matching
// family.* prefix, then the catalog default. Stable/deprecated kinds default to
// denylist; new and unknown kinds default to an empty allowlist. The catalog has
// no privacy-approved path lists, so callers supply AllowedPaths explicitly.
//
// Without an explicit override, stdout.raw, stderr.raw and stdin.write fail
// closed as opaque because encoded bytes cannot be reliably inspected. A
// producer may override them only when it guarantees inspectable JSON text.
// All other payloads must be JSON, including text encoded as a JSON string.
// Policies and their path slices are copied; invalid policies fail construction.
func NewPayloadSanitizer(policies map[string]redact.Policy, opts ...redact.Option) (PayloadSanitizer, error) {
	s := &payloadSanitizer{exact: make(map[string]*redact.Sanitizer), legacy: make(map[string]bool)}
	for key, policy := range policies {
		family := strings.HasSuffix(key, ".*")
		if key == "" || strings.TrimSpace(key) != key || strings.Contains(key, "*") && (!family || strings.Count(key, "*") != 1 || len(key) == 2) {
			return nil, fmt.Errorf("eventstage: invalid sanitizer policy selector")
		}
		sanitizer, err := redact.NewSanitizer(policy, opts...)
		if err != nil {
			return nil, fmt.Errorf("eventstage: invalid sanitizer policy: %w", err)
		}
		if family {
			s.families = append(s.families, familySanitizer{prefix: strings.TrimSuffix(key, "*"), sanitizer: sanitizer})
		} else {
			s.exact[key] = sanitizer
		}
	}
	sort.Slice(s.families, func(i, j int) bool { return len(s.families[i].prefix) > len(s.families[j].prefix) })
	var err error
	if s.allowlist, err = redact.NewSanitizer(redact.Policy{}, opts...); err != nil {
		return nil, err
	}
	if s.denylist, err = redact.NewSanitizer(redact.Policy{Mode: redact.Denylist}, opts...); err != nil {
		return nil, err
	}
	if s.opaque, err = redact.NewSanitizer(redact.Policy{Format: redact.Opaque}, opts...); err != nil {
		return nil, err
	}
	entries, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries.Kinds {
		s.legacy[entry.Kind] = entry.Status == catalog.StatusStable || entry.Status == catalog.StatusDeprecated
	}
	return s, nil
}

type familySanitizer struct {
	prefix    string
	sanitizer *redact.Sanitizer
}
type payloadSanitizer struct {
	exact                       map[string]*redact.Sanitizer
	families                    []familySanitizer
	legacy                      map[string]bool
	allowlist, denylist, opaque *redact.Sanitizer
}

func (s *payloadSanitizer) forKind(kind string) *redact.Sanitizer {
	if exact, ok := s.exact[kind]; ok {
		return exact
	}
	for _, family := range s.families {
		if strings.HasPrefix(kind, family.prefix) && len(kind) > len(family.prefix) {
			return family.sanitizer
		}
	}
	switch kind {
	case "stdout.raw", "stderr.raw", "stdin.write":
		return s.opaque
	}
	if s.legacy[kind] {
		return s.denylist
	}
	return s.allowlist
}

func (s *payloadSanitizer) SanitizePayload(ctx context.Context, kind string, payload []byte) ([]byte, PayloadReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, PayloadReport{}, err
	}
	out, report := s.forKind(kind).Sanitize(payload)
	return out, PayloadReport{Redactions: report.Redactions, Paths: report.Paths, Sensitivity: string(report.Sensitivity), PII: report.PII, ErrorClass: string(report.ErrorClass)}, nil
}
