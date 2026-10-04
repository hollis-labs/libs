//go:build !linux && !darwin

package materialize

import "os"

func installedIdentity(os.FileInfo) string                    { return "" }
func InstalledIdentity(os.FileInfo) string                    { return "" }
func openInstalledRegular(*os.Root, string) (*os.File, error) { return nil, ErrUnsupportedOperation }
