package shim

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func ignoreTermination() { signal.Ignore(syscall.SIGTERM) }
func fingerprintForTest(v Inject) string {
	sum := sha256.Sum256(body(v))
	return hex.EncodeToString(sum[:])
}
func processAlive(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return false
	}
	// On Linux an unreaped grandchild can remain as a zombie under a container
	// init. It no longer executes or owns files; absence of reaping isn't survival.
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		parts := strings.Split(string(data), ") ")
		if len(parts) > 1 && strings.HasPrefix(parts[1], "Z") {
			return false
		}
	}
	return true
}
