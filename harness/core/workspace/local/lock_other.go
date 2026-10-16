//go:build !linux && !darwin

package local

import (
	"context"
	"errors"
	"github.com/hollis-labs/libs/harness/core/workspace"
)

func (p *ports) acquireFile(context.Context, string) (workspace.HeldLock, error) {
	return nil, errors.New("local: locking unsupported on this platform")
}
