//go:build unix && !linux && !darwin

package procuse

// No supported filesystem-type observation is implemented for this platform.
func scannableFilesystem(string) bool { return false }
