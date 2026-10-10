//go:build !linux && !darwin

package snapshot

import "os"

func openRestoreParent(*os.Root, string) (*os.File, error) { return nil, ErrStoreCustodyUnsupported }
func stageRestoreFile(*restoreFile) error                  { return ErrStoreCustodyUnsupported }
func replaceRestoreFile(*restoreFile) error                { return ErrStoreCustodyUnsupported }
