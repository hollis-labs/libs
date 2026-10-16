//go:build unix

package goldens

import (
	"sync"
	"syscall"
)

var fixtureUmask sync.Mutex

// The environment and umask are process-wide. Sandbox consumers run serially;
// this lock also prevents overlapping sandbox lifetimes from changing modes.
func pinFixtureUmask() func() {
	fixtureUmask.Lock()
	previous := syscall.Umask(0022)
	return func() { syscall.Umask(previous); fixtureUmask.Unlock() }
}
