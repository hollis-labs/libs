package retention

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Day, Week and Year are the units a config may use beyond Go's time units.
const (
	Day  = 24 * time.Hour
	Week = 7 * Day
	Year = 365 * Day
)

// Duration is a retention age. Zero means forever. In JSON it is a string:
// a Go duration ("36h"), or a number with a d, w or y unit ("180d", "2y"), or
// "0" / "forever".
type Duration time.Duration

// Forever is the zero Duration.
const Forever Duration = 0

// D converts a time.Duration.
func D(d time.Duration) Duration { return Duration(d) }

// ParseDuration parses the config forms of a [Duration].
func ParseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "", "0", "forever":
		return Forever, nil
	}
	unit := map[byte]time.Duration{'d': Day, 'w': Week, 'y': Year}[s[len(s)-1]]
	if unit != 0 {
		n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("retention: bad duration %q", s)
		}
		// A product past MaxInt64 would wrap negative and make every row
		// look expired the moment it is written.
		if n > math.MaxInt64/int64(unit) {
			return 0, fmt.Errorf("retention: duration %q is too long (the maximum is about 290 years)", s)
		}
		return Duration(time.Duration(n) * unit), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("retention: bad duration %q", s)
	}
	return Duration(d), nil
}

// String renders the shortest exact config form.
func (d Duration) String() string {
	t := time.Duration(d)
	switch {
	case t == 0:
		return "forever"
	case t%Year == 0:
		return strconv.FormatInt(int64(t/Year), 10) + "y"
	case t%Week == 0:
		return strconv.FormatInt(int64(t/Week), 10) + "w"
	case t%Day == 0:
		return strconv.FormatInt(int64(t/Day), 10) + "d"
	}
	return t.String()
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON implements json.Unmarshaler. A bare number is rejected: its
// unit would be a guess.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("retention: duration must be a string like \"30d\": %w", err)
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// shorterThan orders durations where zero is the longest.
func shorterThan(a, b Duration) bool {
	if a == 0 {
		return false
	}
	return b == 0 || a < b
}
