//go:build darwin

package snapshot

func replaceRestoreFile(*restoreFile) error { return ErrStoreCustodyUnsupported }
