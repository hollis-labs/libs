package credentials

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"reflect"
	"testing"
)

func TestForeignSidecarPreservesStagedRecovery(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		t.Run(map[int]string{0: "intent", 1: "link_intent", 2: "interrupted"}[n], func(t *testing.T) {
			g, c, p, s := fixture()
			done := apply(t, g, c, p)
			if done.Outcome != effects.Applied {
				t.Fatal(done)
			}
			index := n
			if index == 2 {
				index = 0
			}
			e := s.evidence[index].Clone()
			if n == 2 {
				e.Phase = effects.InterruptedPhase
				e.Outcome = effects.Partial
			}
			clean := Inspect(context.Background(), g, e, c.PreflightContext, p)
			if len(clean.Obligations) == 0 {
				t.Fatal("invalid staged control", clean)
			}
			e.Attachments = []effects.AttachmentEvidence{{Path: "/foreign/work", Created: true, Outcome: effects.Partial}}
			before := e.Clone()
			creates, removes := p.creates, p.removes
			out := Inspect(context.Background(), g, e, c.PreflightContext, p)
			if out.Outcome != effects.Refused || len(out.Evidence.Attachments) != 0 || !reflect.DeepEqual(out.Evidence.Links, e.Links) || !reflect.DeepEqual(e, before) || p.creates != creates || p.removes != removes {
				t.Fatal("shape refusal contract", out)
			}
			if len(out.Obligations) == 0 {
				t.Fatalf("validated staged receipt lost recovery: clean=%s/%v sidecar=%s/%v phase=%s", clean.Outcome, clean.Obligations, out.Outcome, out.Obligations, e.Phase)
			}
		})
	}
}

func TestForeignSidecarClosedIntentAddsNoStagedRecovery(t *testing.T) {
	g, c, p, s := fixture()
	done := apply(t, g, c, p)
	if done.Outcome != effects.Applied {
		t.Fatal(done)
	}
	e := s.evidence[0].Clone()
	e.Phase = effects.AbortedPhase
	e.Outcome = effects.Refused
	e.Attachments = []effects.AttachmentEvidence{{Path: "/foreign/work", Created: true, Outcome: effects.Partial}}
	out := Inspect(context.Background(), g, e, c.PreflightContext, p)
	if out.Outcome != effects.Refused || len(out.Obligations) != 0 || len(out.Evidence.Attachments) != 0 || !reflect.DeepEqual(out.Evidence.Links, e.Links) {
		t.Fatal(out)
	}
}
