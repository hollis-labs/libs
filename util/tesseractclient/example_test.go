package tesseract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	tesseract "github.com/hollis-labs/libs/util/tesseractclient"
	"github.com/hollis-labs/libs/util/tesseractclient/tesseracttest"
)

// exampleFake serves two records for the examples; adopters point New at a
// real Tesseract instead (tesseract.DefaultBaseURL, or "" for the same).
func exampleFake() (*tesseracttest.Fake, func()) {
	fake := tesseracttest.Start(
		tesseracttest.NewRevision("project/demo/knowledge/notes", "one", tesseracttest.Summary("First note")),
		tesseracttest.NewRevision("project/demo/knowledge/notes", "two", tesseracttest.Summary("Second note")),
	)
	return fake, fake.Close
}

func ExampleNew() {
	c := tesseract.New("http://127.0.0.1:8089/", "", tesseract.WithMaxResponseBytes(8<<20))
	fmt.Println(c.BaseURL())
	// Output: http://127.0.0.1:8089
}

func ExampleWithTimeout() {
	// Bound every request to two seconds instead of the 20s default.
	c := tesseract.New("", "", tesseract.WithTimeout(2*time.Second))
	fmt.Println(c.BaseURL())
	// Output: http://127.0.0.1:8089
}

func ExampleWithMaxResponseBytes() {
	fake, done := exampleFake()
	defer done()
	fake.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{Status: http.StatusOK, Body: `{"status":"ok","padding":"xxxxxxxxxxxxxxxxxxxxxxxx"}`})

	err := tesseract.New(fake.URL(), "", tesseract.WithMaxResponseBytes(16)).Health(context.Background())
	fmt.Println(errors.Is(err, tesseract.ErrResponseTooLarge))
	// Output: true
}

func ExampleClient_BaseURL() {
	fmt.Println(tesseract.New("", "").BaseURL())
	// Output: http://127.0.0.1:8089
}

func ExampleClient_Recall() {
	fake, done := exampleFake()
	defer done()
	c := tesseract.New(fake.URL(), "")

	page, err := c.Recall(context.Background(), tesseract.RecallRequest{
		Namespaces: []string{"project/demo/knowledge/*"},
		Ranking:    "chronological",
		Filters:    tesseract.RecallFilters{Statuses: []string{"canonical"}},
		Limit:      10,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, r := range page.Results {
		fmt.Println(r.Revision.MemoryKey, "-", r.Revision.Payload.Summary)
	}
	fmt.Println("truncated:", page.Manifest.Truncated)
	// Output:
	// one - First note
	// two - Second note
	// truncated: false
}

func ExampleClient_RecallAll() {
	fake, done := exampleFake()
	defer done()
	c := tesseract.New(fake.URL(), "")

	revs, complete, err := c.RecallAll(context.Background(),
		tesseract.RecallRequest{Namespaces: []string{"project/demo/knowledge/*"}, Limit: 1}, 100)
	fmt.Println(len(revs), complete, err)
	// Output: 2 true <nil>
}

func ExampleClient_GetCurrent() {
	fake, done := exampleFake()
	defer done()
	c := tesseract.New(fake.URL(), "")

	rev, err := c.GetCurrent(context.Background(), "project/demo/knowledge/notes", "two")
	fmt.Println(rev.Payload.Summary, err)

	_, err = c.GetCurrent(context.Background(), "project/demo/knowledge/notes", "missing")
	fmt.Println(errors.Is(err, tesseract.ErrNotFound))
	// Output:
	// Second note <nil>
	// true
}

func ExampleClient_GetRevision() {
	fake, done := exampleFake()
	defer done()
	c := tesseract.New(fake.URL(), "")

	rev, err := c.GetRevision(context.Background(), "rev-project/demo/knowledge/notes/one")
	fmt.Println(rev.MemoryKey, rev.Status, err)
	// Output: one canonical <nil>
}

func ExampleClient_Deprecate() {
	fake, done := exampleFake()
	defer done()
	c := tesseract.New(fake.URL(), "")

	id := "rev-project/demo/knowledge/notes/one"
	fmt.Println(c.Deprecate(context.Background(), id))
	rev, _ := c.GetRevision(context.Background(), id) // still readable by id
	fmt.Println(rev.Status)
	// Output:
	// <nil>
	// deprecated
}

func ExampleClient_ListNamespaces() {
	fake, done := exampleFake()
	defer done()
	c := tesseract.New(fake.URL(), "")

	namespaces, err := c.ListNamespaces(context.Background(), "project/demo")
	fmt.Println(namespaces, err)
	// Output: [project/demo/knowledge/notes] <nil>
}

func ExampleClient_Health() {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	c := tesseract.New(up.URL, "")
	fmt.Println(c.Health(context.Background()))

	up.Close()
	fmt.Println(errors.Is(c.Health(context.Background()), tesseract.ErrUnavailable))
	// Output:
	// <nil>
	// true
}

func ExampleAPIError() {
	fake, done := exampleFake()
	defer done()

	_, err := tesseract.New(fake.URL(), "").GetRevision(context.Background(), "nope")
	var api *tesseract.APIError
	if errors.As(err, &api) {
		fmt.Println(api.Status, api.Code, errors.Is(err, tesseract.ErrNotFound))
	}
	// Output: 404 not_found true
}

func ExampleRecallFilters() {
	req := tesseract.RecallRequest{
		Namespaces: []string{"project/demo/knowledge/*"},
		Filters:    tesseract.RecallFilters{Tags: []string{"decision"}, ConfidenceMin: 0.8},
	}
	body, _ := json.Marshal(req)
	fmt.Println(string(body))
	// Output: {"namespaces":["project/demo/knowledge/*"],"filters":{"Tags":["decision"],"ConfidenceMin":0.8}}
}
