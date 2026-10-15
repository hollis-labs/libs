package guard

import "time"

// Clock is the time source of every type in this package. Tests inject a
// fake one; production code uses the default, which is time.Now.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// SystemClock is the wall clock, the default for every constructor here.
var SystemClock Clock = realClock{}

func clockOr(c Clock) Clock {
	if c == nil {
		return SystemClock
	}
	return c
}
