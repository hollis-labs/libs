package retention

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// Class names. Operators may add classes of their own, but events are only
// ever classified into the four defaults.
const (
	ClassAudit    = "R-audit"
	ClassState    = "R-state"
	ClassActivity = "R-activity"
	ClassDebug    = "R-debug"
)

// Defaults for the two guarded values.
const (
	// DefaultPIIPayloadMax is how long a payload flagged pii may stay whole.
	DefaultPIIPayloadMax = 30 * Day
	// AuditFullFloor is the shortest Full an audit class may have without an
	// Acknowledge: the producer's own system of record keeps 400 days.
	AuditFullFloor = 400 * Day
)

// Rollup is one downsampling schedule: counts per Bucket-sized time bucket,
// kept for Keep (zero = forever).
type Rollup struct {
	Bucket Duration `json:"bucket"`
	Keep   Duration `json:"keep"`
}

// ClassPolicy is the retention of one class. Full is how long the whole row
// stays; Envelope is how long the envelope stays after that, measured from
// the event's age, not from the end of Full (zero = forever). A class with
// Full zero keeps whole rows forever and Envelope is ignored.
type ClassPolicy struct {
	Full     Duration `json:"full"`
	Envelope Duration `json:"envelope"`
	Rollups  []Rollup `json:"rollups,omitempty"`
}

// PIIOverride bounds how long a pii payload stays whole, in any class.
type PIIOverride struct {
	// PayloadMax nil keeps the default; an explicit zero means forever (which
	// needs Acknowledge).
	PayloadMax *Duration `json:"payload_max,omitempty"`
}

// Overrides are cross-class rules.
type Overrides struct {
	PII PIIOverride `json:"pii"`
	// StoreSecretPayloads exists only so a config that asks for it can be
	// rejected loudly. It can never be true.
	StoreSecretPayloads bool `json:"store_secret_payloads,omitempty"`
}

// Config is the operator-facing retention configuration. It is merged over
// [Defaults]: a class or field left out keeps its default.
type Config struct {
	Classes   map[string]ClassPolicy `json:"classes,omitempty"`
	Overrides *Overrides             `json:"overrides,omitempty"`
	// Acknowledge permits the two guarded changes (a longer pii payload cap,
	// a shorter audit Full). It does not change what Validate reports as a
	// warning.
	Acknowledge bool `json:"acknowledge,omitempty"`
}

// Policy is a validated, fully resolved configuration.
type Policy struct {
	Classes  map[string]ClassPolicy
	PIIMax   Duration
	Warnings []string
}

// Defaults are the proposed D7 tiers.
func Defaults() Config {
	hourly2y := Rollup{Bucket: D(time.Hour), Keep: D(2 * Year)}
	daily := Rollup{Bucket: D(Day), Keep: Forever}
	return Config{
		Classes: map[string]ClassPolicy{
			ClassAudit:    {Full: Forever, Rollups: []Rollup{daily}},
			ClassState:    {Full: D(180 * Day), Envelope: D(2 * Year), Rollups: []Rollup{hourly2y, daily}},
			ClassActivity: {Full: D(30 * Day), Envelope: D(180 * Day), Rollups: []Rollup{hourly2y, daily}},
			ClassDebug:    {Full: D(7 * Day), Envelope: D(7 * Day), Rollups: []Rollup{{Bucket: D(time.Hour), Keep: D(90 * Day)}}},
		},
		Overrides: &Overrides{PII: PIIOverride{PayloadMax: ptr(D(DefaultPIIPayloadMax))}},
	}
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("retention: invalid configuration")

// Resolve merges c over [Defaults] and validates the result. Shortening any
// value is always allowed. A returned Policy carries Warnings for guarded
// changes that were acknowledged.
func (c Config) Resolve() (*Policy, error) {
	def := Defaults()
	classes := make(map[string]ClassPolicy, len(def.Classes))
	for k, v := range def.Classes {
		classes[k] = v
	}
	for k, v := range c.Classes {
		classes[k] = v
	}
	piiMax := *def.Overrides.PII.PayloadMax
	if c.Overrides != nil {
		if c.Overrides.StoreSecretPayloads {
			return nil, fmt.Errorf("%w: secret payloads are never stored and this cannot be configured", ErrInvalid)
		}
		if c.Overrides.PII.PayloadMax != nil {
			piiMax = *c.Overrides.PII.PayloadMax
		}
	}

	var errs []error
	var warns []string
	known := []string{ClassAudit, ClassState, ClassActivity, ClassDebug}
	names := make([]string, 0, len(classes))
	for k := range classes {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		p := classes[name]
		if !slices.Contains(known, name) {
			errs = append(errs, fmt.Errorf("unknown class %q (events are only classified into %v)", name, known))
			continue
		}
		if p.Full < 0 || p.Envelope < 0 {
			errs = append(errs, fmt.Errorf("class %s: durations must not be negative", name))
			continue
		}
		if p.Full == 0 && p.Envelope != 0 {
			warns = append(warns, fmt.Sprintf("class %s keeps whole rows forever, so its envelope age %s never applies", name, p.Envelope))
		}
		for i, r := range p.Rollups {
			if r.Keep < 0 {
				errs = append(errs, fmt.Errorf("class %s: rollup %d keep must not be negative", name, i))
			}
		}
		if p.Full != 0 && p.Envelope != 0 && p.Envelope < p.Full {
			errs = append(errs, fmt.Errorf("class %s: envelope age %s is shorter than full age %s", name, p.Envelope, p.Full))
		}
		for i, r := range p.Rollups {
			if r.Bucket <= 0 {
				errs = append(errs, fmt.Errorf("class %s: rollup %d needs a positive bucket", name, i))
			}
		}
		if hasDuplicateBuckets(p.Rollups) {
			errs = append(errs, fmt.Errorf("class %s: duplicate rollup bucket", name))
		}
	}
	if piiMax < 0 {
		errs = append(errs, errors.New("pii payload_max must not be negative"))
	}
	// A class with a finite whole-row and envelope age but no rollups is
	// tombstoned and then stays: nothing is removed without a covering rollup.
	for _, name := range names {
		c := classes[name]
		if name != ClassAudit && c.Full != 0 && c.Envelope != 0 && len(c.Rollups) == 0 {
			warns = append(warns, fmt.Sprintf("class %s has no rollups; its rows are tombstoned but never removed", name))
		}
	}
	// Guarded changes.
	if piiMax == Forever || piiMax > D(DefaultPIIPayloadMax) {
		if !c.Acknowledge {
			errs = append(errs, fmt.Errorf("pii payload_max %s is longer than %s: set acknowledge to allow it", piiMax, Duration(DefaultPIIPayloadMax)))
		} else {
			warns = append(warns, fmt.Sprintf("pii payloads may stay whole for %s (default %s)", piiMax, Duration(DefaultPIIPayloadMax)))
		}
	}
	if a := classes[ClassAudit]; a.Full != Forever && a.Full < D(AuditFullFloor) {
		if !c.Acknowledge {
			errs = append(errs, fmt.Errorf("%s full %s is shorter than %s: set acknowledge to allow it", ClassAudit, a.Full, Duration(AuditFullFloor)))
		} else {
			warns = append(warns, fmt.Sprintf("%s keeps whole rows for only %s (floor %s)", ClassAudit, a.Full, Duration(AuditFullFloor)))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return &Policy{Classes: classes, PIIMax: piiMax, Warnings: warns}, nil
}

func ptr[T any](v T) *T { return &v }

func hasDuplicateBuckets(rs []Rollup) bool {
	seen := make([]Duration, 0, len(rs))
	for _, r := range rs {
		if slices.Contains(seen, r.Bucket) {
			return true
		}
		seen = append(seen, r.Bucket)
	}
	return false
}
