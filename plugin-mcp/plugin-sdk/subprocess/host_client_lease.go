package subprocess

import "time"

// Lease metadata is request-local host evidence, not an authority ledger.
func (s *requestScope) initLease(expiry time.Time) {
	if expiry.IsZero() {
		return
	}
	now := time.Now()
	end := now.Add(expiry.Sub(now))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseEnd.IsZero() || end.Before(s.leaseEnd) {
		s.leaseEnd = end
	}
}
func (s *requestScope) leaseDeadline() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.leaseEnd
	if !s.leaseBudgetEnd.IsZero() && (end.IsZero() || s.leaseBudgetEnd.Before(end)) {
		end = s.leaseBudgetEnd
	}
	return end
}
func (s *requestScope) reserveRenew() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renewPending {
		return false
	}
	s.renewPending = true
	return true
}
func (s *requestScope) releaseRenew() { s.mu.Lock(); s.renewPending = false; s.mu.Unlock() }
func (s *requestScope) applyRenew(result BindingsRenewResult, received time.Time, requested uint64) bool {
	expiry, err := time.Parse(time.RFC3339Nano, result.ExpiresAt)
	if err != nil || received.IsZero() {
		return false
	}
	end := received.Add(expiry.Sub(received))
	cap := received.Add(time.Duration(requested) * time.Millisecond)
	if cap.Before(end) {
		end = cap
	}
	budget := received.Add(time.Duration(result.RemainingBudgets.TimeoutMS) * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal || s.ctx.Err() != nil || s.binding == nil || *s.binding != result.BindingID {
		return false
	}
	s.leaseEnd = end
	if s.leaseBudgetEnd.IsZero() || budget.Before(s.leaseBudgetEnd) {
		s.leaseBudgetEnd = budget
	}
	narrow := func(old, next *uint64) *uint64 {
		if next == nil {
			return old
		}
		if old != nil && *old < *next {
			return old
		}
		v := *next
		return &v
	}
	s.leaseBudgets = HostRPCRemainingBudgets{TimeoutMS: result.RemainingBudgets.TimeoutMS, Bytes: narrow(s.leaseBudgets.Bytes, result.RemainingBudgets.Bytes), Effects: narrow(s.leaseBudgets.Effects, result.RemainingBudgets.Effects), Tokens: narrow(s.leaseBudgets.Tokens, result.RemainingBudgets.Tokens)}
	return true
}
