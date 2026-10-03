package transportparity

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// HTTPJSON invokes h directly and decodes a JSON response body. It calls
// h.ServeHTTP with an httptest.ResponseRecorder, so no listener, port or
// network is involved; path may carry a query string. body, when not nil, is
// sent as a JSON request body with Content-Type application/json.
//
// It returns the status and the response body as a json.RawMessage (nil for an
// empty body). A body that is not valid JSON fails t and returns a nil body, so
// a handler that answers plain text is reported instead of silently compared.
// A body that cannot be marshaled fails t.
func HTTPJSON(t T, h http.Handler, method, path string, body any) (status int, decoded json.RawMessage) {
	t.Helper()
	var payload *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("transportparity: HTTPJSON: cannot marshal request body %T: %v", body, err)
			return 0, nil
		}
		payload = bytes.NewReader(raw)
	} else {
		payload = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, payload)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := bytes.TrimSpace(rec.Body.Bytes())
	if len(out) == 0 {
		return rec.Code, nil
	}
	if !json.Valid(out) {
		t.Errorf("transportparity: HTTPJSON: %s %s answered %d with a body that is not JSON: %q", method, path, rec.Code, out)
		return rec.Code, nil
	}
	return rec.Code, json.RawMessage(out)
}
