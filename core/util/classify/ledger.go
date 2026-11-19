package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hollis-labs/libs/core/util/ledger"
)

// Abstained is the ledger label of a facet that gave no answer.
const Abstained = "abstain"

// LedgerRecords turns a result into one ledger row per facet (stage
// "classifier"), in facet-name order.
//
//   - SubjectRef is the input's id, which is required. The version is the pack's
//     name and version label; its hash is the engine fingerprint with the facet
//     and phase, so each facet at each phase is its own decision, and a replay
//     of the same engine over the same input appends nothing.
//   - Input is the classified Input as JSON, shaped by the retention rule for
//     the classifier stage.
//   - Output carries the label (or "abstain"), the probabilities, the top
//     label's probability as confidence, and the votes as detail.
//   - Disposition is Suggested: what is done with an answer is the consumer's
//     decision, recorded as an event on the row.
func (r Result) LedgerRecords(in Input, ret ledger.Retention, env ledger.Envelope, pointer string, keep bool, now time.Time) ([]ledger.Record, error) {
	if r.InputID == "" {
		return nil, errors.New("classify: a ledger record needs the input's id")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	shaped, err := ret.Shape(ledger.StageClassifier, raw, pointer, keep, now)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(r.Facets))
	for n := range r.Facets {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ledger.Record, 0, len(names))
	for _, n := range names {
		fr := r.Facets[n]
		h := sha256.Sum256(fmt.Appendf(nil, "%d:%s|%d:%s|%d:%s", len(r.Hash), r.Hash, len(n), n, len(r.Phase), r.Phase))
		v := ledger.Version{Name: r.Pack + "/" + n, Label: r.Version, Hash: hex.EncodeToString(h[:])}
		if v.Label == "" {
			v.Label = "unversioned"
		}
		detail, err := json.Marshal(struct {
			Phase     Phase    `json:"phase"`
			Top       string   `json:"top"`
			Margin    float64  `json:"margin"`
			Decided   string   `json:"decided,omitempty"`
			Votes     []Vote   `json:"votes,omitempty"`
			Failed    []string `json:"failed,omitempty"`
			ElapsedNs int64    `json:"elapsed_ns"`
		}{r.Phase, fr.Top, fr.Margin, fr.Decided, fr.Votes, r.Failed, int64(r.Elapsed)})
		if err != nil {
			return nil, err
		}
		label := fr.Label
		if label == "" {
			label = Abstained
		}
		p := fr.P
		rec := ledger.Record{
			IdempotencyKey: ledger.Key(r.InputID, ledger.StageClassifier, v),
			Stage:          ledger.StageClassifier,
			SubjectRef:     r.InputID,
			Input:          shaped,
			Version:        v,
			Output:         ledger.Output{Label: label, Probabilities: fr.Probabilities, Confidence: &p, Detail: detail},
			Disposition:    ledger.Suggested,
			Envelope:       env,
			CreatedAt:      now,
		}
		for _, l := range sortedLabels(fr.Probabilities) {
			rec.Alternatives = append(rec.Alternatives, ledger.Alternative{Label: l, Matched: l == fr.Label,
				Why: fmt.Sprintf("p=%.3f", fr.Probabilities[l])})
		}
		out = append(out, rec)
	}
	return out, nil
}

func sortedLabels(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for l := range m {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] > m[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
