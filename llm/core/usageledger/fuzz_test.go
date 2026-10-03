package usageledger

import "testing"

func FuzzValidate(f *testing.F) {
	f.Add("tool_input", int64(5), "measured", int64(0), "unknown")
	f.Add("OUTPUT_TOKENS", int64(-1), "", int64(3), "estimated")
	f.Add("", int64(1<<62), "bogus", int64(1<<62), "measured")
	f.Fuzz(func(t *testing.T, key string, tokens int64, prov string, tokens2 int64, prov2 string) {
		u := NewUsage()
		u.OutputTokens = Component{Tokens: tokens2, Provenance: Provenance(prov2)}
		u.SetDim(key, Component{Tokens: tokens, Provenance: Provenance(prov)})
		err := u.Validate() // must not panic
		_ = u.TotalTokens()
		_ = u.TotalProvenance()
		if err == nil {
			// Validate's contract: accepted values are self-consistent.
			for _, c := range u.core() {
				if !c.Provenance.Valid() || c.Tokens < 0 || (c.Provenance == ProvenanceUnknown && c.Tokens != 0) {
					t.Fatalf("accepted inconsistent core %s %+v", c.name, c.Component)
				}
			}
			if d := u.Dims[key]; !d.Provenance.Valid() || d.Tokens < 0 || (d.Provenance == ProvenanceUnknown && d.Tokens != 0) {
				t.Fatalf("accepted inconsistent dim %+v", d)
			}
		}
	})
}
