package tether

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hollis-labs/go-ssekit"
	messaging "github.com/hollis-labs/substrate/mesh/messaging"
)

type framingTransport func(*http.Request) (*http.Response, error)

func (f framingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func framingClient(body io.ReadCloser) *Client {
	return MustNew("http://sse.test", WithHTTPClient(&http.Client{
		Transport: framingTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{}, Request: r}, nil
		}),
	}))
}

func TestClientSSEFraming(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		want         []string
	}{
		{"multiline", "event: message\ndata:{\"id\":\"one\",\ndata: \"delta\":\"one\"}\n\n", []string{"one"}},
		{"crlf", "data:{\"id\":\"one\",\"delta\":\"one\"}\r\n\r\n", []string{"one"}},
		{"lone_cr", "data:{\"id\":\"one\",\"delta\":\"one\"}\r\r", []string{"one"}},
		{"bom_and_control", "\xef\xbb\xbf: ping\nid: opaque\nretry: 10\nevent: ignored\n\ndata:{\"id\":\"one\",\"delta\":\"one\"}\n\n", []string{"one"}},
		{"consecutive", "data:{\"id\":\"one\",\"delta\":\"one\"}\n\ndata: {\"id\":\"two\",\"delta\":\"two\"}\n\n", []string{"one", "two"}},
		{"incomplete_line", "data: {\"id\":\"lost\",\"delta\":\"lost\"}", nil},
		{"incomplete_block", "data: {\"id\":\"lost\",\"delta\":\"lost\"}\n", nil},
		{"complete_then_incomplete", "data:{\"id\":\"one\",\"delta\":\"one\"}\n\ndata: {\"id\":\"lost\",\"delta\":\"lost\"}\n", []string{"one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("messaging", func(t *testing.T) {
				c := framingClient(io.NopCloser(strings.NewReader(tc.stream)))
				ch, err := c.AsStore().Subscribe(context.Background(), messaging.Address{Kind: messaging.KindAgent, Authority: "test", ID: "recipient"}, messaging.Filter{})
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for env := range ch {
					got = append(got, env.ID)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("envelopes = %q, want %q", got, tc.want)
				}
			})
			t.Run("ai", func(t *testing.T) {
				c := framingClient(io.NopCloser(strings.NewReader(tc.stream)))
				ch, errs, err := c.AIChatStream(context.Background(), ChatRequest{})
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for event := range ch {
					got = append(got, event.Delta)
				}
				for err := range errs {
					t.Fatal(err)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("deltas = %q, want %q", got, tc.want)
				}
			})
		})
	}
}

func TestSubscribeWaitsForBlankLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer func() { _ = reader.Close() }()
		defer func() { _ = writer.Close() }()
		ch, err := framingClient(reader).AsStore().Subscribe(context.Background(), messaging.Address{}, messaging.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, "data: {\"id\":\"one\"}\n"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case env := <-ch:
			t.Fatalf("dispatched before blank line: %+v", env)
		default:
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			t.Fatal(err)
		}
		if env := <-ch; env.ID != "one" {
			t.Fatalf("envelope after blank line: %+v", env)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if env, ok := <-ch; ok {
			t.Fatalf("extra envelope: %+v", env)
		}
	})
}

func TestSubscribeSkipsMalformedEventAndAcceptsLargeEnvelope(t *testing.T) {
	large := strings.Repeat("x", 70*1024)
	stream := "data: not-json\n\ndata: {\"id\":\"large\",\"payload\":\"" + large + "\"}\n\n"
	ch, err := framingClient(io.NopCloser(strings.NewReader(stream))).AsStore().Subscribe(context.Background(), messaging.Address{}, messaging.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if got.ID != "large" || string(got.Payload) != `"`+large+`"` {
		t.Fatal("large envelope was not delivered intact after malformed event")
	}
	if _, ok := <-ch; ok {
		t.Fatal("unexpected extra event")
	}
}

func TestAIStreamReportsDecodeAndFramingErrors(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		tooLarge     bool
	}{
		{"decode", "data: not-json\n\n", false},
		{"framing_limit", "data: " + strings.Repeat("x", ssekit.DefaultMaxEventBytes) + "\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch, errs, err := framingClient(io.NopCloser(strings.NewReader(tc.stream))).AIChatStream(context.Background(), ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if ev, ok := <-ch; ok {
				t.Fatalf("unexpected event: %+v", ev)
			}
			got, ok := <-errs
			if !ok || got == nil || (tc.tooLarge && !errors.Is(got, ssekit.ErrEventTooLarge)) {
				t.Fatalf("stream error = %v", got)
			}
			if _, ok := <-errs; ok {
				t.Fatal("extra error")
			}
		})
	}
}

func TestSubscribeCancellationClosesIncompleteStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: {\"id\":\"unfinished\"}\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := MustNew(srv.URL).AsStore().Subscribe(ctx, messaging.Address{}, messaging.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case env, ok := <-ch:
		if ok {
			t.Fatalf("dispatched incomplete event during cancellation: %+v", env)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not close on cancellation")
	}
}
