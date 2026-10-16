package retention_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/ui/ui-go/streamhub/retention"
)

func mustResolve(t *testing.T, c retention.Config) *retention.Policy {
	t.Helper()
	p, err := c.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return p
}

func TestDurationRoundTrip(t *testing.T) {
	for in, want := range map[string]retention.Duration{
		"0": 0, "forever": 0, "": 0, "36h": retention.D(36 * time.Hour), "180d": retention.D(180 * retention.Day),
		"2y": retention.D(2 * retention.Year), "3w": retention.D(3 * retention.Week), "90m": retention.D(90 * time.Minute),
	} {
		got, err := retention.ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	// Overflow is an error, never a wrapped negative duration.
	for _, big := range []string{"300y", "106752d", "9999999999999999d", "1000000w", "15251w", "9223372036854775807d", "9223372036854775807y", "2562048h"} {
		if d, err := retention.ParseDuration(big); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want an overflow error", big, d)
		}
	}
	for _, edge := range []string{"290y", "106751d", "15250w"} { // just inside int64 nanoseconds (about 292 years)
		if d, err := retention.ParseDuration(edge); err != nil || d <= 0 {
			t.Errorf("ParseDuration(%q) = %v, %v", edge, d, err)
		}
	}
	for _, bad := range []string{"-1d", "d", "1.5d", "10x", "abc"} {
		if _, err := retention.ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) succeeded", bad)
		}
	}
	b, _ := json.Marshal(retention.D(180 * retention.Day))
	if string(b) != `"180d"` {
		t.Errorf("Marshal = %s", b)
	}
	var d retention.Duration
	if err := json.Unmarshal([]byte(`30`), &d); err == nil {
		t.Error("a bare number must be rejected: its unit is a guess")
	}
}

func TestDefaultsAreTheD7Table(t *testing.T) {
	p := mustResolve(t, retention.Config{})
	if len(p.Warnings) != 0 {
		t.Fatalf("defaults warn: %v", p.Warnings)
	}
	cases := []struct {
		class         string
		full, env     retention.Duration
		rollups       int
		firstKeep, d0 retention.Duration
	}{
		{retention.ClassAudit, 0, 0, 1, 0, 0},
		{retention.ClassState, retention.D(180 * retention.Day), retention.D(2 * retention.Year), 2, retention.D(2 * retention.Year), 0},
		{retention.ClassActivity, retention.D(30 * retention.Day), retention.D(180 * retention.Day), 2, retention.D(2 * retention.Year), 0},
		{retention.ClassDebug, retention.D(7 * retention.Day), retention.D(7 * retention.Day), 1, retention.D(90 * retention.Day), 0},
	}
	for _, c := range cases {
		got := p.Classes[c.class]
		if got.Full != c.full || got.Envelope != c.env || len(got.Rollups) != c.rollups {
			t.Errorf("%s = %+v", c.class, got)
		}
	}
	if p.PIIMax != retention.D(30*retention.Day) {
		t.Errorf("pii max = %v", p.PIIMax)
	}
}

func TestConfigMergesOverDefaultsAndShorteningIsFree(t *testing.T) {
	var c retention.Config
	if err := json.Unmarshal([]byte(`{"classes":{"R-activity":{"full":"3d","envelope":"30d","rollups":[{"bucket":"1d","keep":"1y"}]}}}`), &c); err != nil {
		t.Fatal(err)
	}
	p := mustResolve(t, c)
	if a := p.Classes[retention.ClassActivity]; a.Full != retention.D(3*retention.Day) || len(a.Rollups) != 1 {
		t.Fatalf("activity = %+v", a)
	}
	if s := p.Classes[retention.ClassState]; s.Full != retention.D(180*retention.Day) {
		t.Fatalf("an unmentioned class lost its default: %+v", s)
	}
}

func TestGuardedChangesNeedAcknowledge(t *testing.T) {
	long := retention.D(90 * retention.Day)
	short := retention.ClassPolicy{Full: retention.D(30 * retention.Day)}
	for name, c := range map[string]retention.Config{
		"pii cap lengthened": {Overrides: &retention.Overrides{PII: retention.PIIOverride{PayloadMax: &long}}},
		"pii cap forever":    {Overrides: &retention.Overrides{PII: retention.PIIOverride{PayloadMax: new(retention.Duration)}}},
		"audit full < 400d":  {Classes: map[string]retention.ClassPolicy{retention.ClassAudit: short}},
	} {
		if _, err := c.Resolve(); !errors.Is(err, retention.ErrInvalid) || !strings.Contains(err.Error(), "acknowledge") {
			t.Errorf("%s: err = %v, want a refusal that names acknowledge", name, err)
		}
		c.Acknowledge = true
		p, err := c.Resolve()
		if err != nil || len(p.Warnings) != 1 {
			t.Errorf("%s acknowledged: %v, warnings %v; want one warning", name, err, p)
		}
	}
	// Shortening needs nothing.
	sh := retention.D(7 * retention.Day)
	if _, err := (retention.Config{Overrides: &retention.Overrides{PII: retention.PIIOverride{PayloadMax: &sh}}}).Resolve(); err != nil {
		t.Errorf("shortening the pii cap: %v", err)
	}
}

func TestSecretPayloadsAreRejectedEvenAcknowledged(t *testing.T) {
	c := retention.Config{Acknowledge: true, Overrides: &retention.Overrides{StoreSecretPayloads: true}}
	if _, err := c.Resolve(); !errors.Is(err, retention.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestStructuralValidation(t *testing.T) {
	for name, cp := range map[string]retention.ClassPolicy{
		"envelope before full": {Full: retention.D(30 * retention.Day), Envelope: retention.D(7 * retention.Day)},
		"zero bucket":          {Full: retention.D(retention.Day), Rollups: []retention.Rollup{{Bucket: 0, Keep: retention.D(retention.Day)}}},
		"duplicate bucket": {Full: retention.D(retention.Day), Rollups: []retention.Rollup{
			{Bucket: retention.D(time.Hour)}, {Bucket: retention.D(time.Hour)}}},
	} {
		c := retention.Config{Classes: map[string]retention.ClassPolicy{retention.ClassState: cp}}
		if _, err := c.Resolve(); !errors.Is(err, retention.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestClassOf(t *testing.T) {
	p := mustResolve(t, retention.Config{})
	for a, want := range map[retention.Attrs]string{
		{Category: "audit"}:                      retention.ClassAudit,
		{Category: "approval"}:                   retention.ClassAudit,
		{Category: "state"}:                      retention.ClassState,
		{Category: "message"}:                    retention.ClassState,
		{Category: "tool"}:                       retention.ClassActivity,
		{Category: "progress"}:                   retention.ClassActivity,
		{Category: ""}:                           retention.ClassActivity,
		{Category: "mystery"}:                    retention.ClassActivity,
		{Category: "state", Severity: "debug"}:   retention.ClassDebug, // 7d beats 180d
		{Category: "tool", Severity: "debug"}:    retention.ClassDebug,
		{Category: "state", Severity: "warning"}: retention.ClassState,
	} {
		if got := p.ClassOf(a); got != want {
			t.Errorf("ClassOf(%+v) = %s, want %s", a, got, want)
		}
	}
}

// D7 rev 2.1: a severity tag never shortens audit retention, and debug moves
// every other class to R-debug (even when an operator made R-debug longer).
func TestDebugSeverityNeverDowngradesAudit(t *testing.T) {
	p := mustResolve(t, retention.Config{})
	for _, cat := range []string{"audit", "approval"} {
		if got := p.ClassOf(retention.Attrs{Category: cat, Severity: "debug"}); got != retention.ClassAudit {
			t.Errorf("%s with debug severity = %s, want R-audit", cat, got)
		}
		if got := p.TierAt(retention.Attrs{Category: cat, Severity: "debug"}, 10*retention.Year); got != retention.TierFull {
			t.Errorf("%s debug at 10y = %v, want full", cat, got)
		}
	}
	long := retention.Config{Classes: map[string]retention.ClassPolicy{
		retention.ClassDebug: {Full: retention.D(400 * retention.Day), Envelope: retention.D(400 * retention.Day)}}}
	pl := mustResolve(t, long)
	if got := pl.ClassOf(retention.Attrs{Category: "state", Severity: "debug"}); got != retention.ClassDebug {
		t.Errorf("debug state = %s, want R-debug even when R-debug is configured longer", got)
	}
}

// The pii cap applies to the payload only; an audit event keeps its envelope
// forever.
func TestPIIAuditCapsPayloadKeepsEnvelopeForever(t *testing.T) {
	p := mustResolve(t, retention.Config{})
	a := retention.Attrs{Category: "audit", PII: true}
	if got := p.PayloadMax(a); got != retention.D(30*retention.Day) {
		t.Fatalf("payload max = %v, want 30d", got)
	}
	for age, want := range map[time.Duration]retention.Tier{
		29 * retention.Day:  retention.TierFull,
		31 * retention.Day:  retention.TierEnvelope,
		50 * retention.Year: retention.TierEnvelope, // never trimmed: the envelope follows R-audit
	} {
		if got := p.TierAt(a, age); got != want {
			t.Errorf("pii audit at %v = %v, want %v", age, got, want)
		}
	}
	// A debug-severity pii audit event is still R-audit.
	if got := p.ClassOf(retention.Attrs{Category: "audit", PII: true, Severity: "debug"}); got != retention.ClassAudit {
		t.Errorf("class = %s", got)
	}
}

func TestTierAt(t *testing.T) {
	p := mustResolve(t, retention.Config{})
	act := retention.Attrs{Category: "tool"}
	for age, want := range map[time.Duration]retention.Tier{
		0:                   retention.TierFull,
		29 * retention.Day:  retention.TierFull,
		30 * retention.Day:  retention.TierEnvelope,
		179 * retention.Day: retention.TierEnvelope,
		180 * retention.Day: retention.TierTrimmed,
		10 * retention.Year: retention.TierTrimmed,
	} {
		if got := p.TierAt(act, age); got != want {
			t.Errorf("activity at %v = %v, want %v", age, got, want)
		}
	}
	// Audit never leaves the full tier.
	if got := p.TierAt(retention.Attrs{Category: "audit"}, 50*retention.Year); got != retention.TierFull {
		t.Errorf("audit at 50y = %v", got)
	}
	// Debug goes straight from whole to trimmed: no envelope tier.
	dbg := retention.Attrs{Category: "tool", Severity: "debug"}
	if got := p.TierAt(dbg, 7*retention.Day); got != retention.TierTrimmed {
		t.Errorf("debug at 7d = %v", got)
	}
	// Pii caps the payload: a state event is tombstoned at 30d, not 180d, and its envelope follows the class.
	pii := retention.Attrs{Category: "state", PII: true}
	if got := p.TierAt(pii, 31*retention.Day); got != retention.TierEnvelope {
		t.Errorf("pii state at 31d = %v", got)
	}
	if got := p.PayloadMax(pii); got != retention.D(30*retention.Day) {
		t.Errorf("pii payload max = %v", got)
	}
	if got := p.PayloadMax(retention.Attrs{Category: "state"}); got != retention.D(180*retention.Day) {
		t.Errorf("state payload max = %v", got)
	}
}

// Struct literals bypass the parser, so Resolve rejects negative durations itself.
func TestResolveRejectsNegativeDurations(t *testing.T) {
	neg := retention.Duration(-time.Hour)
	for name, c := range map[string]retention.Config{
		"full":     {Classes: map[string]retention.ClassPolicy{retention.ClassState: {Full: neg}}},
		"envelope": {Classes: map[string]retention.ClassPolicy{retention.ClassState: {Full: retention.D(retention.Day), Envelope: neg}}},
		"rollup":   {Classes: map[string]retention.ClassPolicy{retention.ClassState: {Full: retention.D(retention.Day), Rollups: []retention.Rollup{{Bucket: retention.D(time.Hour), Keep: neg}}}}},
		"pii":      {Overrides: &retention.Overrides{PII: retention.PIIOverride{PayloadMax: &neg}}, Acknowledge: true},
		"audit":    {Classes: map[string]retention.ClassPolicy{retention.ClassAudit: {Full: neg}}, Acknowledge: true},
	} {
		if _, err := c.Resolve(); !errors.Is(err, retention.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// A typo in a class name must not silently leave the real class at its default.
func TestUnknownClassNameIsRejected(t *testing.T) {
	c := retention.Config{Classes: map[string]retention.ClassPolicy{"R-audt": {Full: retention.D(retention.Day)}}}
	_, err := c.Resolve()
	if !errors.Is(err, retention.ErrInvalid) || !strings.Contains(err.Error(), "R-audt") {
		t.Fatalf("err = %v, want ErrInvalid naming the class", err)
	}
}

func TestForeverFullWithFiniteEnvelopeWarns(t *testing.T) {
	c := retention.Config{Classes: map[string]retention.ClassPolicy{
		retention.ClassState: {Full: retention.Forever, Envelope: retention.D(retention.Year)}}}
	p := mustResolve(t, c)
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], retention.ClassState) {
		t.Fatalf("warnings = %v", p.Warnings)
	}
}
