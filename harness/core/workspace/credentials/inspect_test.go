package credentials

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"testing"
)

func TestInterruptedReceiptInspectionNeverReplays(t *testing.T) {
	for _, kind := range []string{"before", "after", "missing", "divergent", "unrecorded"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, s := fixture()
			r := apply(t, g, c, f)
			e := r.Evidence.Clone()
			e.Phase = effects.InterruptedPhase
			e.Outcome = effects.Partial
			want := effects.IntendedAfter
			switch kind {
			case "before":
				e = s.evidence[0].Clone()
				f.links = map[string]LinkObservation{}
				want = effects.Before
			case "missing":
				delete(f.links, "a")
				want = effects.Missing
			case "divergent":
				v := f.links["a"]
				v.Identity = "changed"
				f.links["a"] = v
				want = effects.Divergent
			case "unrecorded":
				e = s.evidence[0].Clone()
				want = effects.Divergent
			}
			creates, removes := f.creates, f.removes
			x := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if len(x.Inspections) != 3 || x.Inspections[0].State != want || (x.Outcome != effects.Partial && x.Outcome != effects.Conflict) || f.creates != creates || f.removes != removes {
				t.Fatalf("inspection failed: %+v", x)
			}
		})
	}
}
