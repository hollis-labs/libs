package retention

import "time"

// Attrs are the event facts retention classifies on. They are the envelope's
// class fields; this package reads no envelope itself.
type Attrs struct {
	Category    string
	Severity    string
	Sensitivity string
	PII         bool
}

// Tier is where an event of a given age sits.
type Tier int

// Tiers, in the order an event passes through them.
const (
	TierFull     Tier = iota // whole row
	TierEnvelope             // payload tombstoned
	TierTrimmed              // eligible for head trim once rolled up
)

func (t Tier) String() string {
	switch t {
	case TierFull:
		return "full"
	case TierEnvelope:
		return "envelope"
	case TierTrimmed:
		return "trimmed"
	}
	return "unknown"
}

// categoryClass maps an envelope category to its class.
var categoryClass = map[string]string{
	"audit": ClassAudit, "approval": ClassAudit,
	"state": ClassState, "message": ClassState, "step": ClassState, "fleet": ClassState, "alert": ClassState,
	"activity": ClassActivity, "tool": ClassActivity, "progress": ClassActivity, "data": ClassActivity,
}

// ClassOf names the retention class of an event. Category picks a class; a
// debug severity also matches R-debug. When several match, the most
// restrictive wins: the one that keeps whole rows for the shortest time. An
// unknown or empty category is R-activity, the shortest of the categorized
// classes that is not debug-only, so nothing falls through unbounded.
func (p *Policy) ClassOf(a Attrs) string {
	class, ok := categoryClass[a.Category]
	if !ok {
		class = ClassActivity
	}
	if a.Severity == "debug" {
		if shorterThan(p.Classes[ClassDebug].Full, p.Classes[class].Full) {
			class = ClassDebug
		}
	}
	return class
}

// PayloadMax is how long an event's payload may stay whole: the class's Full,
// capped by the pii override when the event is flagged pii. Zero is forever.
// Secret payloads are never stored; callers must not rely on this for them.
func (p *Policy) PayloadMax(a Attrs) Duration {
	full := p.Classes[p.ClassOf(a)].Full
	if a.PII && shorterThan(p.PIIMax, full) {
		return p.PIIMax
	}
	return full
}

// TierAt reports the tier of an event of the given age. Age is measured from
// the event's recorded time.
func (p *Policy) TierAt(a Attrs, age time.Duration) Tier {
	c := p.Classes[p.ClassOf(a)]
	full := p.PayloadMax(a)
	if full != 0 && age >= time.Duration(full) {
		if c.Envelope != 0 && age >= time.Duration(c.Envelope) {
			return TierTrimmed
		}
		return TierEnvelope
	}
	return TierFull
}
