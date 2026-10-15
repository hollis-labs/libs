package subprocess

import "context"

// Queue cancellation and selection use the same writer lock. selected means
// bytes may reach the peer, even if the subsequent OS write has not returned.
type publicationTicket struct {
	ctx context.Context
}

func (w *frameWriter) submitCancellable(ctx context.Context, frame []byte, receipt func(error), selected func(), completed func() bool, prepare func() ([]byte, error)) error {
	ticket := &publicationTicket{ctx: ctx}
	if err := w.enqueueTicket(frame, receipt, ticket, selected, prepare); err != nil {
		return err
	}
	context.AfterFunc(ctx, func() {
		if completed() {
			return
		}
		w.mu.Lock()
		for i, item := range w.ordinary.queue {
			if item.ticket == ticket {
				w.ordinary.bytes -= len(item.frame)
				copy(w.ordinary.queue[i:], w.ordinary.queue[i+1:])
				w.ordinary.queue[len(w.ordinary.queue)-1] = publication{}
				w.ordinary.queue = w.ordinary.queue[:len(w.ordinary.queue)-1]
				w.mu.Unlock()
				if item.receipt != nil {
					item.receipt(context.Cause(ctx))
				}
				w.notify()
				return
			}
		}
		active := w.activeTicket == ticket
		w.mu.Unlock()
		if active {
			err := context.Cause(ctx)
			w.abort(err)
			if w.onFailure != nil {
				w.onFailure(err)
			}
		}
	})
	return nil
}
func (w *frameWriter) enqueueTicket(frame []byte, receipt func(error), ticket *publicationTicket, selected func(), prepare func() ([]byte, error)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if w.sealed {
		return errConnectionClosed
	}
	if cause := requestContextFailure(ticket.ctx); cause != nil {
		return cause
	}
	lane := &w.ordinary
	if len(lane.queue)+lane.reservedFrames >= w.limits.Frames || len(frame) > w.limits.Bytes-lane.bytes-lane.reservedBytes {
		return errPublicationFull
	}
	lane.queue = append(lane.queue, publication{frame: frame, receipt: receipt, ticket: ticket, selected: selected, prepare: prepare})
	lane.bytes += len(frame)
	w.notify()
	return nil
}
