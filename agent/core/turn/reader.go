package turn

import (
	"context"
	"errors"
	"fmt"
	"time"

	llmtypes "github.com/hollis-labs/go-llm-types"
)

// ErrTurnTruncated distinguishes transport EOF from a provider terminal event.
var ErrTurnTruncated = errors.New("upstream provider stream ended without a terminal signal")

// Reader owns stream cancellation observation and the provider inactivity
// watchdog. It starts no goroutines. Next and Close are used by one consumer;
// the host retains responsibility for cancelling the provider on every exit.
// ReaderOptions selects host accounting behavior on cancellation. Buffered
// events can contain already incurred usage; draining them never waits for a
// producer, and cancellation remains the next observation after that bounded
// queue. The default gives cancellation immediate precedence.
type ReaderOptions struct{ DrainBufferedOnCancel bool }

type Reader struct {
	ctx          context.Context
	events       <-chan llmtypes.StreamEvent
	idle         time.Duration
	timer        *time.Timer
	closed       bool
	terminal     bool
	options      ReaderOptions
	draining     bool
	bufferedLeft int
}

func NewReader(ctx context.Context, events <-chan llmtypes.StreamEvent, idle time.Duration) (*Reader, error) {
	return NewReaderWithOptions(ctx, events, idle, ReaderOptions{})
}

func NewReaderWithOptions(ctx context.Context, events <-chan llmtypes.StreamEvent, idle time.Duration, options ReaderOptions) (*Reader, error) {
	if idle <= 0 {
		return nil, &TurnError{Kind: TurnErrorStart, Err: errors.New("idle timeout must be positive")}
	}
	return &Reader{ctx: ctx, events: events, idle: idle, timer: time.NewTimer(idle), options: options}, nil
}

// TerminalSeen records only the normalized provider EventDone signal. Channel
// closure and usage stop reasons do not establish canonical turn completion.
func (r *Reader) TerminalSeen() bool { return r.terminal }

func (r *Reader) Close() {
	if !r.closed {
		r.closed = true
		r.timer.Stop()
	}
}

// Next observes cancellation with the selected bounded accounting policy.
// Idle time is measured between events, excluding tool execution.
func (r *Reader) Next() (llmtypes.StreamEvent, bool, error) {
	var zero llmtypes.StreamEvent
	if r.ctx.Err() != nil {
		return r.onCancel()
	}
	if r.closed {
		return zero, false, nil
	}
	select {
	case event, ok := <-r.events:
		if r.ctx.Err() != nil && !r.options.DrainBufferedOnCancel {
			return r.onCancel()
		}
		if !ok {
			r.Close()
			return zero, false, nil
		}
		if !r.timer.Stop() {
			select {
			case <-r.timer.C:
			default:
			}
		}
		r.timer.Reset(r.idle)
		if event.Type == llmtypes.EventDone {
			r.terminal = true
		}
		return event, true, nil
	case <-r.timer.C:
		r.Close()
		return zero, false, &TurnError{Kind: TurnErrorStalled, Err: fmt.Errorf("%w: no provider events for %s", ErrTurnStalled, r.idle)}
	case <-r.ctx.Done():
		return r.onCancel()
	}
}

func (r *Reader) onCancel() (llmtypes.StreamEvent, bool, error) {
	if r.options.DrainBufferedOnCancel && !r.closed {
		if !r.draining {
			r.draining = true
			r.bufferedLeft = len(r.events)
			r.timer.Stop()
		}
		if r.bufferedLeft > 0 {
			select {
			case event, ok := <-r.events:
				r.bufferedLeft--
				if ok {
					if event.Type == llmtypes.EventDone {
						r.terminal = true
					}
					return event, true, nil
				}
			default:
			}
		}
	}
	r.Close()
	return llmtypes.StreamEvent{}, false, &TurnError{Kind: TurnErrorStream, Err: context.Cause(r.ctx)}
}
