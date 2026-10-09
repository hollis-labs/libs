package reflexes

// Recurrence cascade: system default, then per-action-kind override, then
// per-reflex override, least to most specific. The system default is a Go
// constant; the kind and reflex tiers are data on ActionKind and Reflex.

import "time"

// DefaultReflexCooldown is the system-level default cooldown a fired
// reflex must observe before it is eligible to fire again, the
// least-specific level of the cascade.
const DefaultReflexCooldown = 15 * time.Minute

// EffectiveCooldown resolves the cascade for one reflex:
// reflex-level override (reflexOverrideSeconds) wins if set; else the
// action kind's default (kindDefaultSeconds) wins if set; else
// DefaultReflexCooldown.
//
// "Set" means non-nil, not non-zero: a pointer to 0 is an explicit
// "no cooldown" override at that level (time.Duration(0), meaning the
// reflex may re-fire on the very next eligible tick) and must NOT be
// treated the same as an absent (nil) override, which falls through to
// the next, less-specific level of the cascade instead.
func EffectiveCooldown(kindDefaultSeconds, reflexOverrideSeconds *int64) time.Duration {
	if reflexOverrideSeconds != nil {
		return time.Duration(*reflexOverrideSeconds) * time.Second
	}
	if kindDefaultSeconds != nil {
		return time.Duration(*kindDefaultSeconds) * time.Second
	}
	return DefaultReflexCooldown
}

// RecentlyFired reports whether reflex r fired within the last window
// (relative to now), per its stored LastFiredAt. A non-positive window
// (the EffectiveCooldown(...) result for an explicit "no cooldown"
// override, or any kind/reflex resolving to zero) always returns false —
// zero cooldown means "no suppression," not "always suppressed."
func RecentlyFired(r Reflex, now time.Time, window time.Duration) bool {
	if r.LastFiredAt == "" || window <= 0 {
		return false
	}
	ts, err := time.Parse(time.RFC3339, r.LastFiredAt)
	if err != nil {
		return false
	}
	return now.Sub(ts) >= 0 && now.Sub(ts) < window
}
