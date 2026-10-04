//go:build !unix

package procuse

import (
	"context"
	"errors"
)

const outputLimit = 1 << 20

// ExecRunner returns an unsupported-platform error on non-Unix hosts.
func ExecRunner(string) Runner {
	return func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("procuse: Unix process observation required")
	}
}

func platformRoots(string, string) ReasonCode { return ReasonUnsupportedPlatform }
