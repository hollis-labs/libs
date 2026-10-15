package federation

import (
	"context"
	"log/slog"
	"time"
)

// AuditRecord is the forensic record of one federated request: who, what, for
// which authority, and the decision with its reason.
type AuditRecord struct {
	Time time.Time
	// Peer and Fingerprint identify the caller ("<unknown>" and "" when it could
	// not be identified).
	Peer        string
	Fingerprint string
	Op          Op
	// Authority is the authority the call is governed by: the recipient's for a
	// send, otherwise the party of the target envelope this install homes.
	Authority string
	// From and To are the envelope's addresses when the call has one.
	From, To string
	Allowed  bool
	// Reason says why: what was done, or why it was refused. It may name internals
	// (a store error, the authorities a peer holds) and so goes only to the audit
	// log, never to the caller.
	Reason string
}

// SlogAuditor returns an auditor that writes each record to logger: Info for an
// allowed request, Warn for a refused one.
func SlogAuditor(logger *slog.Logger) func(AuditRecord) {
	return func(r AuditRecord) {
		level := slog.LevelInfo
		decision := "allow"
		if !r.Allowed {
			level, decision = slog.LevelWarn, "deny"
		}
		logger.Log(context.Background(), level, "federation request",
			"peer", r.Peer, "cert", shortFingerprint(r.Fingerprint), "op", string(r.Op),
			"authority", r.Authority, "from", r.From, "to", r.To, "decision", decision, "reason", r.Reason)
	}
}

func shortFingerprint(fp string) string {
	if len(fp) > 12 {
		return fp[:12]
	}
	return fp
}
