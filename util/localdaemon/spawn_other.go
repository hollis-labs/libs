//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package localdaemon

import "context"

// Spawn is not supported on this platform; it returns [ErrUnsupported].
func Spawn(context.Context, SpawnOptions) (int, error) { return 0, ErrUnsupported }
