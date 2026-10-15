package trust

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"testing"
)

func TestForeignAttachmentSidecarRefused(t *testing.T) {
	r, c := fixture()
	p := &fakePort{}
	prepared, pre := Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	applied := Apply(context.Background(), prepared, c, p)
	e := applied.Evidence.Clone()
	e.Attachments = []effects.AttachmentEvidence{{Path: "/foreign/work", Created: true, Outcome: effects.Partial}}
	got := Inspect(context.Background(), prepared, c.PreflightContext, p, e)
	if len(got.Obligations) == 0 || len(got.Evidence.Attachments) != 0 || len(e.Attachments) != 1 {
		t.Fatal("foreign sidecar lost recovery or leaked payload", got)
	}
	if got.Outcome != effects.Refused {
		t.Fatalf("foreign attachment sidecar accepted: outcome=%s obligations=%v attachments=%v", got.Outcome, got.Obligations, got.Evidence.Attachments)
	}
}

func TestForeignAttachmentRefusalPreservesTrustEvidence(t *testing.T) {
	r, c := fixture()
	p := &fakePort{}
	ticket, pre := Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	done := Apply(context.Background(), ticket, c, p)
	e := done.Evidence.Clone()
	e.Attachments = []effects.AttachmentEvidence{{Created: true, Outcome: effects.Partial}}
	out := Inspect(context.Background(), ticket, c.PreflightContext, p, e)
	if out.Outcome != effects.Refused || len(out.Obligations) == 0 || len(out.Evidence.Trust) != 1 || out.Evidence.Trust[0] != done.Evidence.Trust[0] {
		t.Fatal("trusted own-kind evidence lost", out)
	}
}
