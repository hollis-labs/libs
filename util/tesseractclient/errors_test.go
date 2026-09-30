package tesseract_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	tesseract "github.com/hollis-labs/go-tesseract-client"
	"github.com/hollis-labs/go-tesseract-client/tesseracttest"
)

func TestA404MatchesErrNotFound(t *testing.T) {
	fake := tesseracttest.New(t)
	for name, f := range map[string]tesseracttest.Failure{
		"status only": {Status: http.StatusNotFound, Body: "gone"},
		"code only":   {Status: http.StatusBadRequest, Code: "not_found", Message: "no such thing"},
		"both":        {Status: http.StatusNotFound, Code: "not_found", Message: "no such thing"},
	} {
		fake.Fail(tesseracttest.RouteRevision, f)
		_, err := tesseract.New(fake.URL(), "").GetRevision(ctx(t), "x")
		if !errors.Is(err, tesseract.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
		if errors.Is(err, tesseract.ErrUnavailable) {
			t.Errorf("%s: a 404 must not match ErrUnavailable", name)
		}
	}
}

func TestServerErrorStatusesAreAPIErrorsNotUnavailable(t *testing.T) {
	// Station's real mapping: ErrUnavailable is reserved for transport, read
	// and decode failures. A 502/503 is Tesseract (or a proxy) answering.
	fake := tesseracttest.New(t)
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		fake.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{Status: status, Body: "<html>upstream</html>"})
		err := tesseract.New(fake.URL(), "").Health(ctx(t))
		var api *tesseract.APIError
		if !errors.As(err, &api) || api.Status != status || api.Code != "" || !strings.Contains(api.Message, "upstream") {
			t.Errorf("status %d: err = %#v", status, err)
		}
		if errors.Is(err, tesseract.ErrUnavailable) || errors.Is(err, tesseract.ErrNotFound) {
			t.Errorf("status %d: matched a sentinel: %v", status, err)
		}
	}
}

func TestEmptyErrorBodyFallsBackToTheStatusText(t *testing.T) {
	fake := tesseracttest.New(t)
	fake.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{Status: http.StatusBadGateway, Body: " "})
	var api *tesseract.APIError
	if err := tesseract.New(fake.URL(), "").Health(ctx(t)); !errors.As(err, &api) || api.Message != "Bad Gateway" {
		t.Errorf("err = %#v, want the status text", err)
	}
}

func TestLongErrorBodyIsTruncated(t *testing.T) {
	fake := tesseracttest.New(t)
	fake.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{Status: 400, Body: strings.Repeat("é", 1000)})
	var api *tesseract.APIError
	if err := tesseract.New(fake.URL(), "").Health(ctx(t)); !errors.As(err, &api) || len(api.Message) > 310 || !strings.HasSuffix(api.Message, "…") {
		t.Fatalf("err = %#v, want a truncated message", err)
	}
	if strings.ContainsRune(api.Message, '�') {
		t.Error("truncation split a multi-byte character")
	}
}

func TestUndecodableSuccessBodyIsUnavailable(t *testing.T) {
	fake := tesseracttest.New(t)
	fake.Fail(tesseracttest.RouteRecall, tesseracttest.Failure{Status: http.StatusOK, Body: `<html>a captive portal</html>`})
	_, err := tesseract.New(fake.URL(), "").Recall(ctx(t), tesseract.RecallRequest{Namespaces: []string{root}})
	if !errors.Is(err, tesseract.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestBearerTokenAgainstAServerThatRequiresOne(t *testing.T) {
	fake := tesseracttest.New(t, questions(1)...)
	fake.RequireToken("secret")
	req := tesseract.RecallRequest{Namespaces: []string{root + "/*"}}

	if _, err := tesseract.New(fake.URL(), "secret").Recall(ctx(t), req); err != nil {
		t.Fatalf("with token: %v", err)
	}
	_, err := tesseract.New(fake.URL(), "").Recall(ctx(t), req)
	var api *tesseract.APIError
	if !errors.As(err, &api) || api.Status != http.StatusUnauthorized || api.Code != "auth_required" {
		t.Fatalf("without token: err = %#v, want 401 auth_required", err)
	}
	if errors.Is(err, tesseract.ErrNotFound) || errors.Is(err, tesseract.ErrUnavailable) {
		t.Errorf("a 401 is neither: %v", err)
	}
	// Header presence, straight off the wire.
	reqs := fake.Requests(tesseracttest.RouteRecall)
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("first Authorization = %q", got)
	}
	if _, present := reqs[1].Header["Authorization"]; present {
		t.Errorf("an empty token must send no Authorization header, got %q", reqs[1].Header["Authorization"])
	}
}

func TestOverCapResponseIsAnErrorNotATruncatedDecode(t *testing.T) {
	// A body that is valid JSON even when cut at the cap (trailing spaces), so
	// a client that merely truncates would decode it and report success.
	padded := `{"results":[],"manifest":{"results_total":0}}` + strings.Repeat(" ", 200)
	fake := tesseracttest.New(t)
	fake.Fail(tesseracttest.RouteRecall, tesseracttest.Failure{Status: http.StatusOK, Body: padded})
	req := tesseract.RecallRequest{Namespaces: []string{root}}

	_, err := tesseract.New(fake.URL(), "", tesseract.WithMaxResponseBytes(100)).Recall(ctx(t), req)
	if !errors.Is(err, tesseract.ErrResponseTooLarge) || !errors.Is(err, tesseract.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrResponseTooLarge (also ErrUnavailable)", err)
	}
	// Exactly at the cap is fine; one byte over is not.
	if _, err := tesseract.New(fake.URL(), "", tesseract.WithMaxResponseBytes(int64(len(padded)))).Recall(ctx(t), req); err != nil {
		t.Errorf("a body exactly at the cap: %v", err)
	}
	if _, err := tesseract.New(fake.URL(), "", tesseract.WithMaxResponseBytes(int64(len(padded)-1))).Recall(ctx(t), req); !errors.Is(err, tesseract.ErrResponseTooLarge) {
		t.Errorf("a body one byte over the cap: err = %v", err)
	}
	// The cap covers error bodies too.
	fake.Fail(tesseracttest.RouteRecall, tesseracttest.Failure{Status: http.StatusBadRequest, Body: strings.Repeat("x", 500)})
	if _, err := tesseract.New(fake.URL(), "", tesseract.WithMaxResponseBytes(100)).Recall(ctx(t), req); !errors.Is(err, tesseract.ErrResponseTooLarge) {
		t.Errorf("an oversize error body: err = %v", err)
	}
}

func TestOptionsIgnoreNonPositiveValues(t *testing.T) {
	fake := tesseracttest.New(t, questions(1)...)
	c := tesseract.New(fake.URL(), "", tesseract.WithTimeout(0), tesseract.WithMaxResponseBytes(-1))
	if _, err := c.Recall(ctx(t), tesseract.RecallRequest{Namespaces: []string{root + "/*"}}); err != nil {
		t.Fatalf("a non-positive option must keep the default, not break the client: %v", err)
	}
}

func TestCancelledContextStopsTheRequest(t *testing.T) {
	fake := tesseracttest.New(t)
	fake.Delay(5 * time.Second)
	c, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	start := time.Now()
	err := tesseract.New(fake.URL(), "").Health(c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Station's mapping: a transport-level failure, cancellation included,
	// is also ErrUnavailable.
	if !errors.Is(err, tesseract.ErrUnavailable) {
		t.Errorf("err = %v, want it to match ErrUnavailable too", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("the call outlived its cancelled context")
	}
}

func TestTimeoutOptionBoundsARequest(t *testing.T) {
	fake := tesseracttest.New(t)
	fake.Delay(5 * time.Second)
	start := time.Now()
	err := tesseract.New(fake.URL(), "", tesseract.WithTimeout(100*time.Millisecond)).Health(ctx(t))
	if !errors.Is(err, tesseract.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("WithTimeout did not bound the request")
	}
}
