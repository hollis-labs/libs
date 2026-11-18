package chat

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Clock is the Service's source of time. Inject one to test expiry without
// waiting; chattest.Clock is a manual one.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine (or from the clock's own advance)
	// once d has passed. stop cancels the call and reports whether it did.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// SystemClock is the wall clock.
func SystemClock() Clock { return systemClock{} }

// RandomID returns prefix, an underscore and 128 random bits in hex. It is the
// default ID source.
func RandomID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read does not fail
	return prefix + "_" + hex.EncodeToString(b[:])
}
