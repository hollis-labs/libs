//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package localdaemon

// removeStaleSocket does nothing here: unix: addresses are unsupported and
// net.Listen("unix", ...) reports its own error.
func removeStaleSocket(string) error { return ErrUnsupported }
