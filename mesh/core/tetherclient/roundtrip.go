package tether

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// roundTrip is doJSON for routes that need more than one success status or a
// non-default error mapping. It returns the status it got so a caller can tell
// a 204 from a 200; out is decoded only for a non-204 answer.
func (c *Client) roundTrip(ctx context.Context, method, path string, body any, ok []int, out any, errFn func(*http.Response) error) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal %s %s request: %w", method, path, err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, wrapIfUnreachable(err)
	}
	defer resp.Body.Close()
	if !slices.Contains(ok, resp.StatusCode) {
		return resp.StatusCode, errFn(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return resp.StatusCode, nil
}
