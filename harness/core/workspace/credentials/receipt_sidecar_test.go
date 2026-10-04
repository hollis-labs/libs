package credentials

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"testing"
)

func TestForeignAttachmentSidecarRefused(t *testing.T) {
	g, c, p, _ := fixture()
	applied := apply(t, g, c, p)
	e := applied.Evidence.Clone()
	e.Attachments = []effects.AttachmentEvidence{{Path: "/foreign/work", Created: true, Outcome: effects.Partial}}
	got := Inspect(context.Background(), g, e, c.PreflightContext, p)
	if len(got.Obligations) == 0 || len(got.Evidence.Attachments) != 0 || len(e.Attachments) != 1 {
		t.Fatal("foreign sidecar lost recovery or leaked payload", got)
	}
	if got.Outcome != effects.Refused {
		t.Fatalf("foreign attachment sidecar accepted: outcome=%s obligations=%v attachments=%v", got.Outcome, got.Obligations, got.Evidence.Attachments)
	}
}

func TestForeignAttachmentRefusalPreservesCreatedLinkEvidence(t *testing.T) {
	g, c, p, _ := fixture()
	done := apply(t, g, c, p)
	e := done.Evidence.Clone()
	e.Attachments = []effects.AttachmentEvidence{{Created: true, Outcome: effects.Partial}}
	out := Inspect(context.Background(), g, e, c.PreflightContext, p)
	if out.Outcome != effects.Refused || len(out.Obligations) == 0 || len(out.Evidence.Links) != len(done.Evidence.Links) {
		t.Fatal(out)
	}
	for n, link := range done.Evidence.Links {
		if out.Evidence.Links[n] != link {
			t.Fatal("trusted own-kind evidence lost")
		}
	}
}
