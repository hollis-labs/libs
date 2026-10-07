package host

import (
	"context"
	"errors"
	"testing"

	"github.com/hollis-labs/plugin-sdk/capability"
)

type auditFunc func(context.Context, AuditEvent) error

func (f auditFunc) Record(ctx context.Context, event AuditEvent) error { return f(ctx, event) }
func TestAuditFailureReportedSeparately(t *testing.T) {
	decision := refusal(capability.CapabilityDenied, capability.StorageWrite)
	for _, sink := range []auditFunc{
		func(context.Context, AuditEvent) error { return errors.New("private sink diagnostic") },
		func(context.Context, AuditEvent) error { panic("private sink diagnostic") },
	} {
		auditor := NewAuditor(sink)
		if auditor.Record(context.Background(), AuditEvent{Outcome: decision.Code}) || auditor.Failures() != 1 {
			t.Fatal("sink failure unreported")
		}
		if decision.Code != capability.CapabilityDenied {
			t.Fatal("audit changed decision")
		}
	}
	caller := &Actor{string(SessionClient), "caller"}
	auditor := NewAuditor(auditFunc(func(_ context.Context, event AuditEvent) error { event.InitiatingCaller.ID = "mutated"; return nil }))
	if !auditor.Record(context.Background(), AuditEvent{InitiatingCaller: caller}) || caller.ID != "caller" || auditor.Failures() != 0 {
		t.Fatal("audit aliases caller or failed")
	}
}
