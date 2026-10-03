//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package localdaemon

// IsAlive always reports false on this platform, which has no supported
// liveness probe. Do not use it as evidence that a daemon is not running.
func IsAlive(int) bool { return false }
