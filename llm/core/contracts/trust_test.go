package contracts

import "testing"

func TestEffectiveTrust(t *testing.T) {
	levels := []Trust{TrustUntrusted, TrustNormal, TrustTrusted} // ascending
	for i, a := range levels {
		for j, b := range levels {
			want := levels[min(i, j)]
			if got := EffectiveTrust(a, b); got != want {
				t.Errorf("EffectiveTrust(%s, %s) = %s, want %s", a, b, got, want)
			}
			if EffectiveTrust(a, b) != EffectiveTrust(b, a) {
				t.Errorf("EffectiveTrust is not commutative for %s, %s", a, b)
			}
		}
	}
}

// An unrecognized or empty value can only lower the result, never raise it.
func TestEffectiveTrustUnknownFailsSafe(t *testing.T) {
	for _, odd := range []Trust{"", "root", "TRUSTED"} {
		for _, other := range []Trust{TrustUntrusted, TrustNormal, TrustTrusted} {
			if got := EffectiveTrust(odd, other); got != TrustUntrusted {
				t.Errorf("EffectiveTrust(%q, %s) = %q, want untrusted", odd, other, got)
			}
		}
	}
}

func TestTrustLiteralsMatchTorque(t *testing.T) {
	if TrustTrusted != "trusted" || TrustNormal != "normal" || TrustUntrusted != "untrusted" {
		t.Error("trust literals must match the values Torque already stores")
	}
}
