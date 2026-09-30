package tesseract_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tesseract "github.com/hollis-labs/go-tesseract-client"
	"github.com/hollis-labs/go-tesseract-client/tesseracttest"
)

const root = "project/example/knowledge"

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func questions(n int) []tesseract.Revision {
	out := make([]tesseract.Revision, n)
	for i := range out {
		out[i] = tesseracttest.NewRevision(root+"/questions", fmt.Sprintf("Q-%03d", i+1),
			tesseracttest.Data(map[string]any{"n": i + 1}))
	}
	return out
}

func TestRecallAllFollowsCursorToTheEnd(t *testing.T) {
	fake := tesseracttest.New(t, questions(5)...)
	c := tesseract.New(fake.URL(), "")

	got, complete, err := c.RecallAll(ctx(t), tesseract.RecallRequest{Namespaces: []string{root + "/*"}, Limit: 2}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(got) != 5 {
		t.Fatalf("complete=%v len=%d, want complete with all 5 records", complete, len(got))
	}
	if n := fake.Calls(tesseracttest.RouteRecall); n != 3 {
		t.Errorf("recall calls = %d, want 3 pages of 2, 2 and 1", n)
	}
}

func TestRecallAllReportsWhenItStopsEarly(t *testing.T) {
	fake := tesseracttest.New(t, questions(9)...)
	c := tesseract.New(fake.URL(), "")

	got, complete, err := c.RecallAll(ctx(t), tesseract.RecallRequest{Namespaces: []string{root + "/*"}, Limit: 2}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Error("complete=true after stopping at the cap; the caller would present a partial set as the whole")
	}
	if len(got) < 3 || len(got) >= 9 {
		t.Errorf("len=%d, want it to stop soon after the cap of 3", len(got))
	}
}

func TestRecallAllDefaultsThePageLimit(t *testing.T) {
	fake := tesseracttest.New(t, questions(1)...)
	if _, _, err := tesseract.New(fake.URL(), "").RecallAll(ctx(t), tesseract.RecallRequest{Namespaces: []string{root + "/*"}}, 10); err != nil {
		t.Fatal(err)
	}
	if body := string(fake.Requests(tesseracttest.RouteRecall)[0].Body); !strings.Contains(body, `"limit":500`) {
		t.Errorf("body = %s, want the default page limit of 500", body)
	}
}

func TestRecallAllRefusesACursorThatDoesNotAdvance(t *testing.T) {
	fake := tesseracttest.New(t)
	fake.Fail(tesseracttest.RouteRecall, tesseracttest.Failure{
		Status: http.StatusOK,
		Body:   `{"results":[],"manifest":{"next_cursor":"same"}}`,
	})
	_, _, err := tesseract.New(fake.URL(), "").RecallAll(ctx(t), tesseract.RecallRequest{Namespaces: []string{root}}, 100)
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("err = %v, want a repeated-cursor error rather than an endless loop", err)
	}
}

func TestRecallReturnsScoreAndManifest(t *testing.T) {
	fake := tesseracttest.New(t, questions(3)...)
	page, err := tesseract.New(fake.URL(), "").Recall(ctx(t), tesseract.RecallRequest{
		Namespaces: []string{root + "/*"}, Query: "Q-002", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].Score == nil || page.Manifest.ResultsTotal != 1 || page.Manifest.Truncated {
		t.Fatalf("page = %+v", page)
	}
}

func TestGetCurrentReturnsTheRecord(t *testing.T) {
	fake := tesseracttest.New(t, questions(2)...)
	c := tesseract.New(fake.URL(), "")

	rev, err := c.GetCurrent(ctx(t), root+"/questions", "Q-002")
	if err != nil {
		t.Fatal(err)
	}
	if rev.MemoryKey != "Q-002" || rev.Payload.Summary == "" || len(rev.Payload.Data) == 0 {
		t.Errorf("revision = %+v", rev)
	}
}

func TestGetCurrentMissingKeyIsNotFound(t *testing.T) {
	fake := tesseracttest.New(t, questions(1)...)
	c := tesseract.New(fake.URL(), "")

	_, err := c.GetCurrent(ctx(t), root+"/questions", "Q-099")
	if !errors.Is(err, tesseract.ErrNotFound) {
		t.Fatalf("err = %v, want it to match ErrNotFound", err)
	}
	if errors.Is(err, tesseract.ErrUnavailable) {
		t.Error("a missing key must not read as Tesseract being down")
	}
	var api *tesseract.APIError
	if !errors.As(err, &api) || api.Code != "not_found" || api.Status != http.StatusNotFound {
		t.Errorf("err = %#v, want the APIError with Tesseract's own code", err)
	}
}

func TestRefusalCarriesTesseractsOwnMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"validation_error","message":"unknown field \"domains\" in request body"}`))
	}))
	defer srv.Close()

	_, err := tesseract.New(srv.URL, "").Recall(ctx(t), tesseract.RecallRequest{Namespaces: []string{root}})
	if err == nil || !strings.Contains(err.Error(), `unknown field "domains"`) || !strings.Contains(err.Error(), "validation_error") {
		t.Fatalf("err = %v, want Tesseract's code and message through", err)
	}
	if errors.Is(err, tesseract.ErrNotFound) || errors.Is(err, tesseract.ErrUnavailable) {
		t.Errorf("a 400 is neither not-found nor unavailable: %v", err)
	}
}

func TestUnreachableTesseractIsUnavailable(t *testing.T) {
	fake := tesseracttest.New(t, questions(1)...)
	c := tesseract.New(fake.URL(), "")
	fake.Close()

	_, err := c.GetCurrent(ctx(t), root+"/questions", "Q-001")
	if !errors.Is(err, tesseract.ErrUnavailable) {
		t.Fatalf("err = %v, want it to match ErrUnavailable", err)
	}
}

func TestBearerTokenIsSentOnlyWhenSet(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if err := tesseract.New(srv.URL, "").Health(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := tesseract.New(srv.URL, "secret").Health(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "" || seen[1] != "Bearer secret" {
		t.Errorf("Authorization headers = %q, want none and then a bearer", seen)
	}
}

func TestListNamespaces(t *testing.T) {
	fake := tesseracttest.New(t,
		tesseracttest.NewRevision(root+"/questions", "Q-001"),
		tesseracttest.NewRevision(root+"/assets", "A-001"),
		tesseracttest.NewRevision(root+"/meta", "M-001"),
		tesseracttest.NewRevision("project/other/knowledge", "unrelated"),
	)
	got, err := tesseract.New(fake.URL(), "").ListNamespaces(ctx(t), root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{root + "/questions": true, root + "/assets": true, root + "/meta": true}
	if len(got) != len(want) {
		t.Fatalf("namespaces = %v", got)
	}
	for _, ns := range got {
		if !want[ns] {
			t.Errorf("unexpected namespace %q", ns)
		}
	}
}

func TestListNamespacesFollowsTheCursor(t *testing.T) {
	fake := tesseracttest.New(t,
		tesseracttest.NewRevision(root+"/a", "1"),
		tesseracttest.NewRevision(root+"/b", "1"),
		tesseracttest.NewRevision(root+"/c", "1"),
	)
	fake.PageSize(2)
	got, err := tesseract.New(fake.URL(), "").ListNamespaces(ctx(t), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || fake.Calls(tesseracttest.RouteNamespaces) != 2 {
		t.Errorf("got %v after %d calls, want 3 namespaces over 2 pages", got, fake.Calls(tesseracttest.RouteNamespaces))
	}
}

func TestHealth(t *testing.T) {
	fake := tesseracttest.New(t)
	c := tesseract.New(fake.URL(), "")
	if err := c.Health(ctx(t)); err != nil {
		t.Fatal(err)
	}
	fake.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{Status: http.StatusInternalServerError, Code: "readiness_failed", Message: "store down"})
	if err := c.Health(ctx(t)); err == nil || !strings.Contains(err.Error(), "readiness_failed") {
		t.Errorf("err = %v, want the readiness refusal", err)
	}
}

func TestNewDefaultsAndOptions(t *testing.T) {
	if got := tesseract.New("", "").BaseURL(); got != tesseract.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want the default", got)
	}
	if got := tesseract.New("http://x:1/", "").BaseURL(); got != "http://x:1" {
		t.Errorf("BaseURL = %q, want the trailing slash trimmed", got)
	}
}
