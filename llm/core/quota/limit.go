package quota

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Unit names what a limit counts, in the provider's or the application's own
// unit: "requests", "input_tokens", "output_tokens", "usd_micros". Amounts are
// never converted between units; a limit counts only usage recorded in its
// own unit.
type Unit string

// Limit is one budget: at most Max of Unit for Key over Window.
//
// Key is the application's name for what is being limited, for example
// "provider=anthropic/model=claude" or "caller=alice". The library treats it
// as opaque.
//
// Max is ignored for provider windows, whose budget comes from the provider's
// reported snapshot (see Limiter.Observe).
type Limit struct {
	Key    string
	Unit   Unit
	Window Window
	Max    int64
}

// Counter returns the counter this limit counts against.
func (l Limit) Counter() Counter {
	return Counter{Key: l.Key, Unit: l.Unit, Window: l.Window.ID()}
}

// Validate reports whether the limit can be checked.
func (l Limit) Validate() error {
	if strings.TrimSpace(l.Key) == "" {
		return errors.New("quota: limit has an empty key")
	}
	if strings.TrimSpace(string(l.Unit)) == "" {
		return errors.New("quota: limit has an empty unit")
	}
	if err := l.Window.Validate(); err != nil {
		return err
	}
	if l.Window.Kind() != KindProvider && l.Max < 0 {
		return fmt.Errorf("quota: limit max %d is negative", l.Max)
	}
	return nil
}

// Counter identifies one stream of usage: a key, a unit and a window ID.
// Stores keep and query usage per Counter.
type Counter struct {
	Key    string
	Unit   Unit
	Window string
}

// String renders the counter as key|unit|window, for logs.
func (c Counter) String() string {
	return c.Key + "|" + string(c.Unit) + "|" + c.Window
}

// Entry is one usage event: Amount of the counter's unit, at At.
type Entry struct {
	At     time.Time
	Amount int64
}

// Unknown is the Remaining value of a Decision when the remaining budget cannot
// be known, which happens only for a provider window without a usable snapshot.
const Unknown int64 = -1

// Reasons a Decision carries. A Decision's Reason is empty when it is allowed
// with a known remaining budget.
const (
	// ReasonExhausted: the request does not fit what remains in the window, but
	// it would fit an empty window; waiting RetryAfter helps.
	ReasonExhausted = "exhausted"
	// ReasonTooLarge: the request is larger than the whole limit; waiting cannot
	// help. The caller has to make the request smaller.
	ReasonTooLarge = "request exceeds the whole limit"
	// ReasonNoSnapshot: a provider window has no snapshot, or its snapshot has
	// passed its reset time without saying what the limit is. The request is
	// allowed and Remaining is Unknown.
	ReasonNoSnapshot = "no provider snapshot"
)

// Decision is the answer to Check or Reserve.
type Decision struct {
	// Allowed reports whether the requested amount fits.
	Allowed bool
	// Remaining is what is left in the window before this request: committed
	// usage and other open reservations are already subtracted, the requested
	// amount is not. It never goes below zero, except for Unknown.
	Remaining int64
	// RetryAfter is how long until the request would fit, when it is denied
	// with ReasonExhausted. It is zero when the request is allowed and when
	// waiting cannot help.
	RetryAfter time.Duration
	// Reason says why the request is denied, or why the decision is uncertain.
	// It is one of the Reason constants, or empty.
	Reason string
}

// ProviderSnapshot is what a provider reported about one of its limits, for
// example from rate-limit response headers.
type ProviderSnapshot struct {
	// Remaining is what the provider says is left at ObservedAt.
	Remaining int64
	// Limit is the window's full budget, when the provider says it; zero when it
	// does not. It lets the limiter keep counting after ResetsAt until a fresh
	// snapshot arrives.
	Limit int64
	// ResetsAt is when the provider's window resets.
	ResetsAt time.Time
	// ObservedAt is when the provider gave the answer. Usage recorded at or
	// after it is subtracted from Remaining; usage before it is assumed to be
	// included in the provider's figure.
	ObservedAt time.Time
}

// Errors returned by the package.
var (
	// ErrReservationClosed: Commit or Cancel on a reservation that was already
	// committed or cancelled.
	ErrReservationClosed = errors.New("quota: reservation already committed or cancelled")
	// ErrNegativeAmount: an amount below zero.
	ErrNegativeAmount = errors.New("quota: negative amount")
	// ErrNotProviderWindow: Observe on a limit whose window is not a provider
	// window.
	ErrNotProviderWindow = errors.New("quota: limit is not a provider window")
)
