//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package localdaemon

import "context"

// Stop is not supported on this platform; it returns [ErrUnsupported].
func Stop(context.Context, int, StopOptions) error { return ErrUnsupported }
