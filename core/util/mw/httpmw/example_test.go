package httpmw_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/core/util/mw/httpmw"
)

func Example() {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stack, err := httpmw.Assemble(mw.Options{},
		httpmw.Recover(log),
		httpmw.RequestID(),
		httpmw.AccessLog(log),
		httpmw.BodyLimit(8),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	h := stack.Then(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, "ok")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("small")))
	fmt.Println(rec.Code, rec.Header().Get(httpmw.RequestIDHeader) != "")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("this body is too large")))
	fmt.Println(rec.Code, strings.TrimSpace(rec.Body.String()))
	// Output:
	// 200 true
	// 413 {"error":{"code":"body_too_large","message":"request body too large"}}
}

func ExampleAssemble_requiresWhatItNeeds() {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := httpmw.Assemble(mw.Options{}, httpmw.AccessLog(log)) // needs request_id from an outer member
	fmt.Println(err)
	// Output:
	// access-log (#0): requires "request_id", which no outer member provides
}
