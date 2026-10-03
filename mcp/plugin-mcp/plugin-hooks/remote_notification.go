package pluginhooks

import (
	"context"
	"errors"
)

// RemoteNotifier submits an observation to a host writer without waiting for a
// remote response. A nil error means submitted, never completed or successful.
// The host must not retry uncertain delivery or authorize callbacks from it.
type RemoteNotifier interface {
	Notify(context.Context, RemoteNotification) error
}
type RemoteNotifierFunc func(context.Context, RemoteNotification) error

func (f RemoteNotifierFunc) Notify(ctx context.Context, n RemoteNotification) error { return f(ctx, n) }

// RemoteNotification deliberately carries no live callback binding. The request
// contains private payload/metadata and a host deadline but grants no callbacks.
type RemoteNotification struct{ Request RemoteRequest }

var errNotificationSubmitted = errors.New("remote notification submitted")
