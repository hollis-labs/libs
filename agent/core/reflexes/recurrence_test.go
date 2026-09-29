package reflexes

import (
	"testing"
	"time"
)

func int64p(v int64) *int64 { return &v }

func TestEffectiveCooldown_SystemDefault_WhenNeitherLevelSet(t *testing.T) {
	got := EffectiveCooldown(nil, nil)
	if got != DefaultReflexCooldown {
		t.Fatalf("EffectiveCooldown(nil, nil) = %v, want system default %v", got, DefaultReflexCooldown)
	}
}

func TestEffectiveCooldown_KindLevelOverridesSystemDefault(t *testing.T) {
	got := EffectiveCooldown(int64p(300), nil)
	want := 300 * time.Second
	if got != want {
		t.Fatalf("EffectiveCooldown(kind=300, reflex=nil) = %v, want %v", got, want)
	}
}

func TestEffectiveCooldown_KindLevelExplicitZero_MeansNoCooldown(t *testing.T) {
	got := EffectiveCooldown(int64p(0), nil)
	if got != 0 {
		t.Fatalf("EffectiveCooldown(kind=0, reflex=nil) = %v, want 0 (explicit zero is a real override, not unset)", got)
	}
}

func TestEffectiveCooldown_ReflexLevelOverridesKindAndSystem(t *testing.T) {
	got := EffectiveCooldown(int64p(300), int64p(900))
	want := 900 * time.Second
	if got != want {
		t.Fatalf("EffectiveCooldown(kind=300, reflex=900) = %v, want %v", got, want)
	}
}

func TestEffectiveCooldown_ReflexLevelExplicitZero_MeansNoCooldown(t *testing.T) {
	got := EffectiveCooldown(int64p(300), int64p(0))
	if got != 0 {
		t.Fatalf("EffectiveCooldown(kind=300, reflex=0) = %v, want 0 (explicit zero override, not unset, and it beats a nonzero kind default)", got)
	}
}

func TestEffectiveCooldown_ReflexLevelExplicitZero_OverridesUnsetKind(t *testing.T) {
	got := EffectiveCooldown(nil, int64p(0))
	if got != 0 {
		t.Fatalf("EffectiveCooldown(kind=nil, reflex=0) = %v, want 0", got)
	}
}

func TestRecentlyFired_ZeroWindowNeverSuppresses(t *testing.T) {
	now := time.Now()
	r := Reflex{LastFiredAt: now.Add(-1 * time.Second).UTC().Format(time.RFC3339)}
	if RecentlyFired(r, now, 0) {
		t.Fatal("RecentlyFired with a zero window must return false — zero cooldown means no suppression")
	}
}

func TestRecentlyFired_WithinWindowSuppresses(t *testing.T) {
	now := time.Now()
	r := Reflex{LastFiredAt: now.Add(-1 * time.Minute).UTC().Format(time.RFC3339)}
	if !RecentlyFired(r, now, 15*time.Minute) {
		t.Fatal("RecentlyFired should suppress a reflex that fired 1 minute ago against a 15-minute window")
	}
}

func TestRecentlyFired_OutsideWindowDoesNotSuppress(t *testing.T) {
	now := time.Now()
	r := Reflex{LastFiredAt: now.Add(-20 * time.Minute).UTC().Format(time.RFC3339)}
	if RecentlyFired(r, now, 15*time.Minute) {
		t.Fatal("RecentlyFired should not suppress a reflex that fired 20 minutes ago against a 15-minute window")
	}
}
