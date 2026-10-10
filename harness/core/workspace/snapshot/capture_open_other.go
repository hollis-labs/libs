//go:build !linux && !darwin

package snapshot

import "os"

func openCaptureFile(_ *os.Root, _ string) (*os.File, error) { return nil, ErrCoverageUnsupported }
