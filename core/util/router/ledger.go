package router

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/libs/core/util/ledger"
)

// LedgerRecord turns a Decision into a ledger row (stage "rule").
//
//   - SubjectRef is the subject id; the idempotency key is subject + stage +
//     rule-set version and hash, so replaying the same rules over the same
//     subject appends nothing, and a changed rule set appends a new row.
//   - Input goes through the retention rule for the rule stage. Decision.Input is
//     already shaped by Config.Snapshot; retention then decides snapshot, pointer
//     or hash-only, and any redaction. pointer names where the original lives.
//   - Output.Label is the sorted, comma-joined targets of the matches that were
//     not denied, or "no_route" / "denied". Detail keeps the full match list
//     with dispositions, obligations, policy provenance and shadow verdicts.
//   - Disposition: routed -> applied; suggested -> suggested; denied and
//     no_route -> rejected (the system declined; a human decision is a
//     separate row).
func (d Decision) LedgerRecord(ret ledger.Retention, env ledger.Envelope, pointer string, keep bool, now time.Time) (ledger.Record, error) {
	raw, err := json.Marshal(d.Input)
	if err != nil {
		return ledger.Record{}, err
	}
	in, err := ret.Shape(ledger.StageRule, raw, pointer, keep, now)
	if err != nil {
		return ledger.Record{}, err
	}
	detail, err := json.Marshal(struct {
		Outcome        Outcome            `json:"outcome"`
		Strategy       Strategy           `json:"strategy"`
		Matches        []Match            `json:"matches,omitempty"`
		ShadowVerdicts map[string]Verdict `json:"shadow_verdicts,omitempty"`
		Escalation     *Escalation        `json:"escalation,omitempty"`
	}{d.Outcome, d.Strategy, d.Matches, d.ShadowVerdicts, d.Escalation})
	if err != nil {
		return ledger.Record{}, err
	}
	v := ledger.Version{Name: "router", Label: d.RuleVersion, Hash: d.RuleHash}
	rec := ledger.Record{
		IdempotencyKey: ledger.Key(d.SubjectID, ledger.StageRule, v),
		Stage:          ledger.StageRule,
		SubjectRef:     d.SubjectID,
		Input:          in,
		Version:        v,
		Output:         ledger.Output{Label: d.label(), Confidence: d.Input.Confidence, Detail: detail},
		Disposition:    d.disposition(),
		Envelope:       env,
	}
	for _, c := range d.Considered {
		rec.Alternatives = append(rec.Alternatives, ledger.Alternative{Label: c.RuleID, Matched: c.Matched, Why: c.Why})
	}
	return rec, nil
}

func (d Decision) label() string {
	var targets []string
	for _, m := range d.Matches {
		if m.Disposition != Deny {
			targets = append(targets, m.Target)
		}
	}
	switch {
	case len(targets) > 0:
		sort.Strings(targets)
		return strings.Join(targets, ",")
	case d.Outcome == Denied:
		return "denied"
	}
	return "no_route"
}

func (d Decision) disposition() ledger.Disposition {
	switch d.Outcome {
	case Routed:
		return ledger.Applied
	case Suggested:
		return ledger.Suggested
	}
	return ledger.Rejected
}
