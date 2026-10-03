package transportparity_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	svcerr "github.com/hollis-labs/go-svcerr"
	tp "github.com/hollis-labs/go-transportparity"
)

// printT lets an Example show what a failing assertion would say.
type printT struct{}

func (printT) Helper()                   {}
func (printT) Errorf(f string, a ...any) { fmt.Println("FAIL:", fmt.Sprintf(f, a...)) }
func (printT) Fatalf(f string, a ...any) { fmt.Println("FATAL:", fmt.Sprintf(f, a...)) }

// The motivating case: one human-gate decision, two doors. The MCP door checks
// the caller's scope; the HTTP door forgot to. Same input, different outcome.
func ExampleAssertSameOutcome() {
	httpDoor := func(scope string) tp.Outcome { // no scope check
		return tp.FromValue(map[string]any{"decided": true})
	}
	mcpDoor := func(scope string) tp.Outcome {
		if scope != "human_gate:write" {
			return tp.FromError(svcerr.New(svcerr.CodePermission, "scope missing"), svcerr.CodeInternal, 403)
		}
		return tp.FromValue(map[string]any{"decided": true})
	}

	tp.AssertSameOutcome(printT{}, "decide with the right scope", httpDoor("human_gate:write"), mcpDoor("human_gate:write"))
	tp.AssertSameOutcome(printT{}, "decide without the scope", httpDoor(""), mcpDoor(""))
	// Output:
	// FAIL: decide without the scope: transports disagree on success: A ok, B failed category="permission" status=403 detail="permission: scope missing"
}

func ExampleFromError() {
	o := tp.FromError(svcerr.New(svcerr.CodeNotFound, "no such gate"), svcerr.CodeInternal, 500)
	fmt.Println(o.Category, o.Status)
	// Output: not_found 404
}

func ExampleFromValue() {
	fmt.Println(tp.FromValue(map[string]any{"id": 1}))
	// Output: ok
}

func ExampleCanonicalJSON() {
	type row struct {
		Zebra string `json:"zebra"`
		Alpha int    `json:"alpha"`
	}
	fmt.Println(tp.CanonicalJSON(printT{}, row{Zebra: "z", Alpha: 1}))
	fmt.Println(tp.CanonicalJSON(printT{}, map[string]any{"zebra": "z", "alpha": 1.0}))
	// Output:
	// {"alpha":1,"zebra":"z"}
	// {"alpha":1,"zebra":"z"}
}

func ExampleHTTPJSON() {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path})
	})
	status, body := tp.HTTPJSON(printT{}, h, http.MethodGet, "/v1/gates", nil)
	fmt.Println(status, string(body))
	// Output: 200 {"path":"/v1/gates"}
}

func ExampleAcceptedFieldNames() {
	type request struct {
		MemoryKey string `json:"memory_key"`
		Owner     struct {
			ID string `json:"id"`
		} `json:"owner"`
	}
	fmt.Println(strings.Join(tp.AcceptedFieldNames(request{}), ", "))
	// Output: memory_key, owner, owner.id
}

func ExampleAssertSameFieldNames() {
	type mcpArgs struct {
		Key string `json:"key"`
	}
	type httpRequest struct {
		MemoryKey string `json:"memory_key"`
	}
	tp.AssertSameFieldNames(printT{}, "MCP", "HTTP", tp.AcceptedFieldNames(mcpArgs{}), tp.AcceptedFieldNames(httpRequest{}))
	// Output:
	// FAIL: accepted fields differ between MCP and HTTP:
	//   only MCP: [key]
	//   only HTTP: [memory_key]
}
