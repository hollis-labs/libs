package contracts

// Trust is how far an assignment or a definition is trusted. The values match
// the literals Torque already stores.
type Trust string

// The trust levels.
const (
	TrustTrusted   Trust = "trusted"
	TrustNormal    Trust = "normal"
	TrustUntrusted Trust = "untrusted"
)

// Valid reports whether t is one of the three trust levels.
func (t Trust) Valid() bool {
	switch t {
	case TrustTrusted, TrustNormal, TrustUntrusted:
		return true
	default:
		return false
	}
}

// rank orders trust: untrusted < normal < trusted. Anything that is not a
// defined level — including the empty string — ranks as untrusted, so an
// unrecognized value can only lower the result, never raise it.
func (t Trust) rank() int {
	switch t {
	case TrustTrusted:
		return 2
	case TrustNormal:
		return 1
	default:
		return 0
	}
}

// EffectiveTrust returns the LOWER (less privileged) of two trust values. It is
// commutative, and it has no parameter through which a force flag could raise
// the result.
func EffectiveTrust(a, b Trust) Trust {
	low := a
	if b.rank() < a.rank() {
		low = b
	}
	if !low.Valid() {
		return TrustUntrusted
	}
	return low
}
