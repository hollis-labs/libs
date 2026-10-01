//go:build unix

package tether

import (
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

func readTokenFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("tether: open credential file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("tether: stat credential file: %w", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ok || int(owner.Uid) != os.Getuid() {
		return "", fmt.Errorf("tether: credential file must be a regular file owned by this user with mode 0600")
	}
	body, err := io.ReadAll(io.LimitReader(f, 258))
	if err != nil {
		return "", fmt.Errorf("tether: read credential file: %w", err)
	}
	token := strings.TrimSpace(string(body))
	if len(body) >= 258 || !validBearerToken(token) {
		return "", fmt.Errorf("tether: invalid credential file content")
	}
	return token, nil
}
