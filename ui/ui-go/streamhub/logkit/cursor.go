package logkit

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// ErrBadCursor means a cursor string is not "<epoch>:<seq>" (or empty).
var ErrBadCursor = errors.New("logkit: malformed cursor")

// FormatCursor renders the opaque cursor "<epoch>:<seq>" that a consumer
// stores and presents back to a log's AfterCursor.
func FormatCursor(epoch string, seq streamhub.Seq) string {
	return epoch + ":" + strconv.FormatUint(uint64(seq), 10)
}

// ParseCursor splits a cursor made by [FormatCursor]. The empty string means
// "from the start" and yields an empty epoch and Seq 0.
func ParseCursor(c string) (epoch string, seq streamhub.Seq, err error) {
	if c == "" {
		return "", 0, nil
	}
	i := strings.LastIndexByte(c, ':')
	if i <= 0 {
		return "", 0, fmt.Errorf("%w: %q", ErrBadCursor, c)
	}
	n, perr := strconv.ParseUint(c[i+1:], 10, 64)
	if perr != nil {
		return "", 0, fmt.Errorf("%w: %q", ErrBadCursor, c)
	}
	return c[:i], streamhub.Seq(n), nil
}
