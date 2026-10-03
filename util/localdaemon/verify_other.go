//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package localdaemon

import "context"

// VerifyCommand is not supported on this platform; it returns
// [ErrUnsupported].
func VerifyCommand(context.Context, int, Matcher) (bool, error) { return false, ErrUnsupported }
