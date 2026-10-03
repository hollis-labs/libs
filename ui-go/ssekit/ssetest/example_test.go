package ssetest_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"time"

	"github.com/hollis-labs/libs/ui-go/ssekit/ssetest"
)

func ExampleScript() {
	// Connection 1: two events, then the line is cut. Connection 2: 404.
	srv := httptest.NewServer(ssetest.Script(ssetest.Drop(2), ssetest.Status(404)))
	defer srv.Close()

	for range 2 {
		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			fmt.Println(err)
			return
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Println(resp.StatusCode, len(body) > 0, err != nil)
	}
	// Output:
	// 200 true true
	// 404 true false
}

func ExampleReplay() {
	frames := []ssetest.Frame{
		{At: 0, Raw: []byte("id: 1\ndata: a\n\n")},
		{At: 10 * time.Millisecond, Raw: []byte(": keepalive\n\n")},
	}
	var buf bytes.Buffer
	_ = ssetest.Replay(context.Background(), &buf, frames)
	fmt.Printf("%q\n", buf.String())
	// Output: "id: 1\ndata: a\n\n: keepalive\n\n"
}
