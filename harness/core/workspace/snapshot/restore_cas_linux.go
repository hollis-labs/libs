//go:build linux

package snapshot

import "golang.org/x/sys/unix"

func replaceRestoreFile(f *restoreFile) error {
	flags := uint(unix.RENAME_NOREPLACE)
	if f.selection.Expected.Present {
		flags = unix.RENAME_EXCHANGE
	}
	if e := unix.Renameat2(int(f.parent.Fd()), f.stage, int(f.parent.Fd()), f.name, flags); e != nil {
		return ErrRestoreUncertain
	}
	// Exchange retains the original file under the recorded staging name. There
	// is no destructive cleanup or automatic compensation on this restore path.
	return nil
}
