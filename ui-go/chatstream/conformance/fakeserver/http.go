package fakeserver

import (
	"context"
	"fmt"
	"iter"
	"net/http"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/framing"
)

// StatusError is DecodeOverHTTP's error for a response that is not 200 OK.
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("fakeserver: upstream answered %d %s", e.Code, http.StatusText(e.Code))
}

// HTTPOption configures DecodeOverHTTP.
type HTTPOption func(*httpConfig)

type httpConfig struct{ client *http.Client }

// WithClient sets the HTTP client (default http.DefaultClient); use Upstream.Client
// for a Memory upstream.
func WithClient(c *http.Client) HTTPOption { return func(h *httpConfig) { h.client = c } }

// DecodeOverHTTP GETs url as an event stream, cuts it into frames with
// framing.SSE and decodes them with a decoder from adapter, yielding the events.
// It is chatstream.DecodeFrames over one HTTP connection and nothing more: no
// reconnect, no cursor (that is go-ssekit's Client).
//
// What that gives, whatever the upstream does: the sequence ends after exactly
// one terminal event. A connection that is cut or goes silent until ctx ends
// yields a run.error (upstream_truncated) carrying the cause, never a success.
// Failing to connect, or a status other than 200, yields one error and no events:
// there was no run to terminate.
func DecodeOverHTTP(ctx context.Context, url string, adapter chatstream.Adapter, opts chatstream.DecodeOptions, more ...HTTPOption) iter.Seq2[chatstream.Event, error] {
	cfg := httpConfig{client: http.DefaultClient}
	for _, o := range more {
		o(&cfg)
	}
	return func(yield func(chatstream.Event, error) bool) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			yield(chatstream.Event{}, err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err := cfg.client.Do(req)
		if err != nil {
			yield(chatstream.Event{}, err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			yield(chatstream.Event{}, &StatusError{Code: resp.StatusCode})
			return
		}
		for ev, err := range chatstream.DecodeFrames(ctx, adapter.NewDecoder(opts), framing.SSE(resp.Body)) {
			if !yield(ev, err) {
				return
			}
		}
	}
}
