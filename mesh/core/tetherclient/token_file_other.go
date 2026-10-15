//go:build !unix

package tether

import (
	"fmt"
	"os"
)

func readTokenFile(path string) (string, error) {
	// A missing default file still permits an anonymous/offline client.
	if _, err := os.Lstat(path); err != nil {
		return "", fmt.Errorf("tether: stat credential file: %w", err)
	}
	return "", fmt.Errorf("tether: token files require POSIX ownership and mode 0600; use WithToken or TETHER_TOKEN on this platform")
}
