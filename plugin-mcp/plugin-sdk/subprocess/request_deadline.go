package subprocess

import (
	"context"
	"time"
)

// Timer callbacks are scheduling events, not the deadline itself. Check the
// monotonic clock at admission/publication boundaries even before Done closes.
func requestContextFailure(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if end, ok := ctx.Deadline(); ok && !time.Now().Before(end) {
		return &DeadlineExceededError{}
	}
	return nil
}
