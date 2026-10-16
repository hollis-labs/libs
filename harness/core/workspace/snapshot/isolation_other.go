//go:build !linux

package snapshot

import "context"

// ObserveIsolation has no supported kernel producer on this platform.
func ObserveIsolation(context.Context, IsolationRequest) (*IsolationProof, error) {
	return nil, ErrStoreCustodyUnsupported
}
