package auth

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// Mode selects how the guard treats requests.
type Mode string

const (
	// ModeOff never rejects. A valid token is still verified and stamped;
	// otherwise the synthetic operator(off-mode) principal is stamped and an
	// audit event is emitted.
	ModeOff Mode = "off"
	// ModePermissive verifies and never rejects; it reports "would deny".
	ModePermissive Mode = "permissive"
	// ModeEnforce rejects unauthenticated and under-scoped requests.
	ModeEnforce Mode = "enforce"
)

// strictness orders the modes: enforce is the strictest.
func (m Mode) strictness() int {
	switch m {
	case ModeEnforce:
		return 2
	case ModePermissive:
		return 1
	}
	return 0
}

// WeakerThan reports whether m admits more than other does: off is weaker than
// permissive, which is weaker than enforce. The modes are compared in their
// ParseMode canonical form; an unrecognized mode is treated as enforce, as the
// guard does.
func (m Mode) WeakerThan(other Mode) bool {
	return m.canonical().strictness() < other.canonical().strictness()
}

func (m Mode) canonical() Mode {
	if c, err := ParseMode(string(m)); err == nil {
		return c
	}
	return ModeEnforce
}

// IsLoopbackAddr reports whether addr is reachable only from this machine: a
// unix socket, "localhost", or a loopback IP (127.0.0.0/8, ::1). addr may be
// "host:port", "tcp:host:port" or "unix:/path". It is stricter than
// ValidateBind, which also accepts tailnet addresses: a tailnet address is
// reachable from other machines, so it is NOT loopback here. An address that
// does not parse is not loopback.
func IsLoopbackAddr(addr string) bool {
	if strings.HasPrefix(addr, "unix:") || strings.HasPrefix(addr, "/") {
		return true
	}
	host, _, err := net.SplitHostPort(strings.TrimPrefix(addr, "tcp:"))
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ParseMode accepts off, permissive, enforce, and the alias observe
// (= permissive), case-insensitively.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return ModeOff, nil
	case "permissive", "observe":
		return ModePermissive, nil
	case "enforce":
		return ModeEnforce, nil
	}
	return "", fmt.Errorf("auth: invalid mode %q (want off, permissive or enforce)", s)
}

// ModeFromEnv reads HOLLIS_AUTH_<APP> (per-app override), then HOLLIS_AUTH,
// then returns def. app is upper-cased with non-alphanumerics mapped to "_".
func ModeFromEnv(app string, def Mode) (Mode, error) {
	key := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 32
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, app)
	for _, name := range []string{"HOLLIS_AUTH_" + key, "HOLLIS_AUTH"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return ParseMode(v)
		}
	}
	return def, nil
}

// ValidateBind refuses off and permissive on any listener that is not
// loopback or tailnet-bound. addr may be "host:port", "tcp:host:port" or
// "unix:/path" (always allowed). A wildcard host (":8080", "0.0.0.0") is not
// loopback or tailnet and is refused unless mode is enforce.
func ValidateBind(addr string, mode Mode) error {
	if _, err := ParseMode(string(mode)); err != nil {
		return err
	}
	if mode == ModeEnforce {
		return nil
	}
	if strings.HasPrefix(addr, "unix:") || strings.HasPrefix(addr, "/") {
		return nil
	}
	hp := strings.TrimPrefix(addr, "tcp:")
	host, _, err := net.SplitHostPort(hp)
	if err != nil {
		return fmt.Errorf("auth: parse listen address: %w", err)
	}
	if !localOrTailnet(host) {
		return fmt.Errorf("auth: mode %q refused on non-loopback, non-tailnet listener %q; use enforce", mode, host)
	}
	return nil
}

var tailnetV4 = mustCIDR("100.64.0.0/10")
var tailnetV6 = mustCIDR("fd7a:115c:a1e0::/48")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func localOrTailnet(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".ts.net") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	return ip.IsLoopback() || tailnetV4.Contains(ip) || tailnetV6.Contains(ip)
}
