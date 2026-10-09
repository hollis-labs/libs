package capability

import (
	"bytes"
	"time"
)

// ValidateRenewal checks an unchanged, still-live authority set. Timestamps are
// the only mutable fields. It does not authenticate a host or approve policy;
// the host must revalidate both before sending and after receiving an ack.
// Scope bytes and grant order are retained exactly, including host extensions.
func (s GrantSet) ValidateRenewal(next GrantSet, owner RuntimeIdentity, now time.Time) error {
	if err := s.ValidateForRuntime(owner); err != nil {
		return err
	}
	if err := next.ValidateForRuntime(owner); err != nil {
		return err
	}
	if len(s) != len(next) {
		return invalid("grants", "renewal changed authority")
	}
	for i, old := range s {
		n := next[i]
		oldEnd, _ := time.Parse(time.RFC3339Nano, old.ExpiresAt)
		oldStart, _ := time.Parse(time.RFC3339Nano, old.IssuedAt)
		end, _ := time.Parse(time.RFC3339Nano, n.ExpiresAt)
		start, _ := time.Parse(time.RFC3339Nano, n.IssuedAt)
		if !now.Before(oldEnd) || start.Before(oldStart) || !end.After(oldEnd) {
			return invalid("grants", "renewal requires a live lease and advancing timestamps")
		}
		if old.GrantID != n.GrantID || old.Name != n.Name || old.SchemaVersion != n.SchemaVersion || !bytes.Equal(old.Scope, n.Scope) || old.Audience != n.Audience || old.PolicyRevision != n.PolicyRevision {
			return invalid("grants", "renewal changed authority")
		}
	}
	return nil
}

// Clone returns a detached discovery snapshot; it confers no authority.
func (s GrantSet) Clone() GrantSet {
	next := make(GrantSet, len(s))
	copy(next, s)
	for i := range next {
		next[i].Scope = append([]byte(nil), s[i].Scope...)
	}
	return next
}
