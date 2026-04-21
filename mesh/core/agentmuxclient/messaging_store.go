package agentmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	messaging "github.com/hollis-labs/go-messaging"
)

// httpStore implements messaging.Store over the /messages/* HTTP routes.
// Subscribe is best-effort in-process fan-out: it observes envelopes sent
// through THIS instance only. It does not see messages from other processes.
// Replace with SSE-based Subscribe when /messages/subscribe ships in agent-mux.
type httpStore struct {
	c    *Client
	mu   sync.Mutex
	subs []*msgSub
}

type msgSub struct {
	ch     chan messaging.Envelope
	filter messaging.Filter
	ctx    context.Context
}

// Verify interface at compile time.
var _ messaging.Store = (*httpStore)(nil)

// httpDispatcher wraps httpStore and overrides Request to use the daemon's
// blocking POST /messages/request endpoint instead of Subscribe-based polling.
type httpDispatcher struct {
	*httpStore
}

// Verify interface at compile time.
var _ messaging.Dispatcher = (*httpDispatcher)(nil)

// AsStore returns a messaging.Store backed by this client's /messages/* routes.
func (c *Client) AsStore() messaging.Store {
	return &httpStore{c: c}
}

// AsDispatcher returns a messaging.Dispatcher backed by this client.
// Request uses the daemon's blocking POST /messages/request endpoint.
func (c *Client) AsDispatcher() messaging.Dispatcher {
	return &httpDispatcher{httpStore: &httpStore{c: c}}
}

// Send persists an envelope. The server assigns ID and CreatedAt; caller-set
// values are zeroed before the POST. Returns ErrPresetLifecycle if DeliveredAt
// or ConsumedAt are non-nil.
func (s *httpStore) Send(ctx context.Context, env messaging.Envelope) (messaging.Envelope, error) {
	if env.DeliveredAt != nil || env.ConsumedAt != nil {
		return messaging.Envelope{}, messaging.ErrPresetLifecycle
	}
	env.ID = ""
	env.CreatedAt = time.Time{}

	var out messaging.Envelope
	if err := s.c.doJSON(ctx, http.MethodPost, "/messages", env, http.StatusCreated, &out); err != nil {
		return messaging.Envelope{}, mapStoreError(err)
	}
	s.fanOut(out)
	return out, nil
}

// Get retrieves a single envelope by ID. Returns ErrNotFound if absent.
func (s *httpStore) Get(ctx context.Context, id string) (messaging.Envelope, error) {
	var out messaging.Envelope
	err := s.c.getJSON(ctx, "/messages/"+url.PathEscape(id), &out)
	if err != nil {
		return messaging.Envelope{}, mapStoreError(err)
	}
	return out, nil
}

// Inbox returns undelivered envelopes for to, atomically marking them delivered.
func (s *httpStore) Inbox(ctx context.Context, to messaging.Address, f messaging.Filter) ([]messaging.Envelope, error) {
	q := url.Values{}
	q.Set("to", to.URN())
	if len(f.Kind) > 0 {
		kinds := make([]string, len(f.Kind))
		for i, k := range f.Kind {
			kinds[i] = string(k)
		}
		q.Set("kind", strings.Join(kinds, ","))
	}
	if f.ThreadID != "" {
		q.Set("thread_id", f.ThreadID)
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", f.Limit))
	}

	var resp struct {
		Messages []messaging.Envelope `json:"messages"`
	}
	if err := s.c.getJSON(ctx, withQuery("/messages/inbox", q), &resp); err != nil {
		return nil, mapStoreError(err)
	}
	return resp.Messages, nil
}

// Thread returns envelopes sharing a threadID. Read-only; no delivery side effects.
func (s *httpStore) Thread(ctx context.Context, threadID string, f messaging.Filter) ([]messaging.Envelope, error) {
	q := url.Values{}
	if len(f.Kind) > 0 {
		kinds := make([]string, len(f.Kind))
		for i, k := range f.Kind {
			kinds[i] = string(k)
		}
		q.Set("kind", strings.Join(kinds, ","))
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", f.Limit))
	}

	var resp struct {
		Messages []messaging.Envelope `json:"messages"`
	}
	if err := s.c.getJSON(ctx, withQuery("/messages/thread/"+url.PathEscape(threadID), q), &resp); err != nil {
		return nil, mapStoreError(err)
	}
	return resp.Messages, nil
}

// Consume advances ConsumedAt for (envelope, recipient). Idempotent.
func (s *httpStore) Consume(ctx context.Context, id string, recipient messaging.Address) error {
	q := url.Values{}
	q.Set("as", recipient.URN())
	path := withQuery("/messages/"+url.PathEscape(id)+"/consume", q)
	return mapStoreError(s.c.doNoBody(ctx, http.MethodPost, path, nil, http.StatusNoContent))
}

// Cancel marks an envelope dead. Idempotent. Returns ErrNotFound if absent.
func (s *httpStore) Cancel(ctx context.Context, id string) error {
	err := s.c.doNoBody(ctx, http.MethodPost, "/messages/"+url.PathEscape(id)+"/cancel", nil, http.StatusNoContent)
	return mapStoreError(err)
}

// Subscribe returns a channel that receives envelopes sent through this httpStore
// after the subscription is created. Closes when ctx is canceled.
//
// Polling-based; replace with SSE when /messages/subscribe ships in agent-mux.
func (s *httpStore) Subscribe(ctx context.Context, f messaging.Filter) (<-chan messaging.Envelope, error) {
	sub := &msgSub{
		ch:     make(chan messaging.Envelope, 16),
		filter: f,
		ctx:    ctx,
	}
	s.mu.Lock()
	s.subs = append(s.subs, sub)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, sv := range s.subs {
			if sv == sub {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				break
			}
		}
		close(sub.ch)
	}()

	return sub.ch, nil
}

// fanOut broadcasts env to all matching in-process subscribers. Non-blocking:
// drops for a subscriber whose buffer is full.
func (s *httpStore) fanOut(env messaging.Envelope) {
	s.mu.Lock()
	snapshot := make([]*msgSub, len(s.subs))
	copy(snapshot, s.subs)
	s.mu.Unlock()

	for _, sub := range snapshot {
		if !sub.filter.Matches(env) {
			continue
		}
		select {
		case sub.ch <- env:
		case <-sub.ctx.Done():
		default:
		}
	}
}

// Request sends env as Kind=request and blocks until the daemon returns a
// matching response or ctx expires. Uses POST /messages/request for true
// blocking semantics; returns ErrRequestTimeout on 504.
func (d *httpDispatcher) Request(ctx context.Context, env messaging.Envelope) (messaging.Envelope, error) {
	env.Kind = messaging.MsgKindRequest
	env.ID = ""

	q := url.Values{}
	if deadline, ok := ctx.Deadline(); ok {
		if timeout := time.Until(deadline); timeout > 0 {
			q.Set("timeout", timeout.String())
		}
	}

	var out messaging.Envelope
	err := d.c.doJSON(ctx, http.MethodPost, withQuery("/messages/request", q), env, http.StatusOK, &out)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusGatewayTimeout {
			return messaging.Envelope{}, messaging.ErrRequestTimeout
		}
		return messaging.Envelope{}, mapStoreError(err)
	}
	return out, nil
}

// Reply constructs and sends a response envelope to parent.
func (d *httpDispatcher) Reply(ctx context.Context, parent messaging.Envelope, payload json.RawMessage) (messaging.Envelope, error) {
	resp := messaging.Envelope{
		Kind:        messaging.MsgKindResponse,
		From:        parent.To,
		To:          parent.From,
		ThreadID:    parent.ThreadID,
		InReplyTo:   parent.ID,
		Payload:     payload,
		ContentType: "application/json",
	}
	return d.httpStore.Send(ctx, resp)
}

// mapStoreError translates HTTP API errors into canonical messaging sentinel errors.
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == "not_found" || apiErr.StatusCode == http.StatusNotFound:
			return fmt.Errorf("%w: %w", messaging.ErrNotFound, err)
		case apiErr.Code == "preset_lifecycle" || apiErr.StatusCode == http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %w", messaging.ErrPresetLifecycle, err)
		}
	}
	return err
}
