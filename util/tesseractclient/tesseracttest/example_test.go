package tesseracttest_test

import (
	"context"
	"fmt"

	tesseract "github.com/hollis-labs/go-tesseract-client"
	"github.com/hollis-labs/go-tesseract-client/tesseracttest"
)

func ExampleNew() {
	// In a test: fake := tesseracttest.New(t, ...) closes itself on cleanup.
	// Here Start stands in for it, as an Example has no testing.T.
	fake := tesseracttest.Start(tesseracttest.NewRevision("project/demo/knowledge", "hello"))
	defer fake.Close()

	rev, err := tesseract.New(fake.URL(), "").GetCurrent(context.Background(), "project/demo/knowledge", "hello")
	fmt.Println(rev.Payload.Summary, err)
	// Output: Summary of hello <nil>
}

func ExampleStart() {
	fake := tesseracttest.Start()
	defer fake.Close()

	fake.Fail(tesseracttest.RouteReadiness, tesseracttest.Failure{Status: 503, Code: "unavailable", Message: "warming up"})
	fmt.Println(tesseract.New(fake.URL(), "").Health(context.Background()))
	fmt.Println(fake.Calls(tesseracttest.RouteReadiness))
	// Output:
	// tesseract refused /v1/health/readiness (503): unavailable: warming up
	// 1
}

func ExampleFake_Fail() {
	fake := tesseracttest.Start()
	defer fake.Close()
	fake.Fail(tesseracttest.RouteRecall, tesseracttest.Failure{Status: 500, Body: "boom"})

	_, err := tesseract.New(fake.URL(), "").Recall(context.Background(), tesseract.RecallRequest{Namespaces: []string{"a"}})
	fmt.Println(err)
	// Output: tesseract refused /v1/memory/recall (500): boom
}

func ExampleFake_RequireToken() {
	fake := tesseracttest.Start(tesseracttest.NewRevision("a", "k"))
	defer fake.Close()
	fake.RequireToken("s3cret")

	_, err := tesseract.New(fake.URL(), "").ListNamespaces(context.Background(), "")
	fmt.Println(err)
	ns, err := tesseract.New(fake.URL(), "s3cret").ListNamespaces(context.Background(), "")
	fmt.Println(ns, err)
	// Output:
	// tesseract refused /v1/namespaces/list (401): auth_required: missing or invalid bearer token
	// [a] <nil>
}

func ExampleFake_PageSize() {
	fake := tesseracttest.Start(
		tesseracttest.NewRevision("a", "1"), tesseracttest.NewRevision("a", "2"), tesseracttest.NewRevision("a", "3"))
	defer fake.Close()
	fake.PageSize(2)

	revs, complete, _ := tesseract.New(fake.URL(), "").RecallAll(context.Background(),
		tesseract.RecallRequest{Namespaces: []string{"a"}}, 100)
	fmt.Println(len(revs), complete, fake.Calls(tesseracttest.RouteRecall))
	// Output: 3 true 2
}

func ExampleFake_Requests() {
	fake := tesseracttest.Start()
	defer fake.Close()
	_ = tesseract.New(fake.URL(), "tok").Health(context.Background())

	r := fake.Requests(tesseracttest.RouteReadiness)[0]
	fmt.Println(r.Method, r.Path, r.Header.Get("Authorization"))
	// Output: GET /v1/health/readiness Bearer tok
}
