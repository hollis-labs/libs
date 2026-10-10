package quota

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind says how a Window decides which usage counts.
type Kind int

const (
	// KindCalendar windows are calendar periods (day, week, month) in a named
	// time zone. Usage counts from the start of the current period until its end.
	KindCalendar Kind = iota + 1
	// KindRolling windows are a trailing duration: usage counts while it is
	// younger than the duration.
	KindRolling
	// KindProvider windows are reported by the provider: the remaining amount and
	// the reset time come from the provider's own answer (for example rate-limit
	// response headers), not from local counting.
	KindProvider
)

// String returns "calendar", "rolling" or "provider".
func (k Kind) String() string {
	switch k {
	case KindCalendar:
		return "calendar"
	case KindRolling:
		return "rolling"
	case KindProvider:
		return "provider"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Period is the length of a calendar window.
type Period int

const (
	// Day runs from local midnight to the next local midnight.
	Day Period = iota + 1
	// Week runs from local midnight on Monday (ISO 8601) to the next Monday.
	Week
	// Month runs from local midnight on the first of the month to the first of
	// the next month.
	Month
)

// String returns "day", "week" or "month".
func (p Period) String() string {
	switch p {
	case Day:
		return "day"
	case Week:
		return "week"
	case Month:
		return "month"
	default:
		return fmt.Sprintf("Period(%d)", int(p))
	}
}

// Window is a time window a Limit is measured over. Build one with Calendar,
// Rolling or Provider; the zero Window is invalid.
//
// Two windows with different IDs never share a counter: usage recorded against
// a calendar day is not seen by a rolling 24-hour limit on the same key, and a
// provider-reported remaining amount is never mixed into locally counted usage.
type Window struct {
	kind   Kind
	period Period
	loc    *time.Location
	dur    time.Duration
	name   string
}

// Calendar returns a calendar window of period p in loc. A nil loc means UTC.
//
// Boundaries follow the local wall clock in loc. On a daylight-saving change a
// day is 23 or 25 hours long and the limit is not prorated. Where a zone skips
// local midnight, the period starts at the first instant that carries the new
// local date; where midnight occurs twice, it starts at the first of them.
func Calendar(p Period, loc *time.Location) Window {
	if loc == nil {
		loc = time.UTC
	}
	return Window{kind: KindCalendar, period: p, loc: loc}
}

// Rolling returns a trailing window of length d. Usage recorded at t counts
// while now - t < d.
func Rolling(d time.Duration) Window {
	return Window{kind: KindRolling, dur: d}
}

// Provider returns a provider-reported window. name distinguishes several
// provider limits on the same key (for example "requests-per-minute" and
// "input-tokens-per-minute"); it is part of the window's ID.
func Provider(name string) Window {
	return Window{kind: KindProvider, name: name}
}

// Kind returns the window's kind.
func (w Window) Kind() Kind { return w.kind }

// Period returns the calendar period; zero for other kinds.
func (w Window) Period() Period { return w.period }

// Location returns the calendar window's time zone; nil for other kinds.
func (w Window) Location() *time.Location { return w.loc }

// Duration returns the rolling window's length; zero for other kinds.
func (w Window) Duration() time.Duration { return w.dur }

// Name returns the provider window's name; empty for other kinds.
func (w Window) Name() string { return w.name }

// ID identifies the window. It is part of every Counter, so windows with
// different IDs count separately. Examples: "calendar:day:UTC",
// "calendar:month:America/Chicago", "rolling:1m0s", "provider:input-tokens".
func (w Window) ID() string {
	switch w.kind {
	case KindCalendar:
		return "calendar:" + w.period.String() + ":" + w.loc.String()
	case KindRolling:
		return "rolling:" + w.dur.String()
	case KindProvider:
		return "provider:" + w.name
	default:
		return ""
	}
}

// String returns the window's ID.
func (w Window) String() string { return w.ID() }

// Validate reports whether the window was built by one of the constructors with
// usable arguments.
func (w Window) Validate() error {
	switch w.kind {
	case KindCalendar:
		if w.period < Day || w.period > Month {
			return fmt.Errorf("quota: calendar window: unknown period %d", int(w.period))
		}
		if w.loc == nil {
			return errors.New("quota: calendar window: nil location")
		}
	case KindRolling:
		if w.dur <= 0 {
			return fmt.Errorf("quota: rolling window: duration %s is not positive", w.dur)
		}
	case KindProvider:
		if strings.TrimSpace(w.name) == "" {
			return errors.New("quota: provider window: empty name")
		}
	default:
		return errors.New("quota: zero or unknown window; use Calendar, Rolling or Provider")
	}
	return nil
}

// Bounds returns the half-open interval [start, end) of the window that
// contains now. For a rolling window that is (now-d, now], expressed as
// [now-d+1ns, now+1ns). Provider windows have no local bounds and return
// ok == false.
func (w Window) Bounds(now time.Time) (start, end time.Time, ok bool) {
	switch w.kind {
	case KindCalendar:
		start, end = w.calendarBounds(now)
		return start, end, true
	case KindRolling:
		return now.Add(-w.dur + time.Nanosecond), now.Add(time.Nanosecond), true
	default:
		return time.Time{}, time.Time{}, false
	}
}

func (w Window) calendarBounds(now time.Time) (time.Time, time.Time) {
	local := now.In(w.loc)
	y, m, d := local.Date()
	switch w.period {
	case Day:
		return localDayStart(y, m, d, w.loc), localDayStart(y, m, d+1, w.loc)
	case Week:
		// time.Weekday has Sunday = 0; ISO weeks start on Monday.
		back := (int(local.Weekday()) + 6) % 7
		return localDayStart(y, m, d-back, w.loc), localDayStart(y, m, d-back+7, w.loc)
	default: // Month
		return localDayStart(y, m, 1, w.loc), localDayStart(y, m+1, 1, w.loc)
	}
}

// localDayStart returns the first instant whose local date in loc is y-m-d
// (normalized as time.Date normalizes). time.Date alone is not enough: for a
// local midnight that does not exist it may return an instant on the previous
// date, and for one that occurs twice it may return the second.
func localDayStart(y int, m time.Month, d int, loc *time.Location) time.Time {
	target := time.Date(y, m, d, 12, 0, 0, 0, time.UTC) // normalizes the date only
	ty, tm, td := target.Date()
	same := func(t time.Time) bool {
		ly, lm, ld := t.In(loc).Date()
		return ly == ty && lm == tm && ld == td
	}
	t := time.Date(ty, tm, td, 0, 0, 0, 0, loc)
	// Real zone transitions move the clock by at most a few hours, on minute
	// boundaries; the loops are bounded to keep a broken zone from spinning.
	for i := 0; !same(t) && i < 24*60; i++ {
		t = t.Add(time.Minute)
	}
	for i := 0; i < 24*60 && same(t.Add(-time.Minute)); i++ {
		t = t.Add(-time.Minute)
	}
	return t
}
