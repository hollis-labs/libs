package tether_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	tether "github.com/hollis-labs/go-tether-client"
)

func TestRoutingCapabilities(t *testing.T) {
	for _, sessionID := range []string{"", "session/with space"} {
		t.Run(sessionID, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/routing/capabilities" || r.URL.Query().Get("session_id") != sessionID {
					t.Errorf("URL = %s", r.URL)
				}
				fmt.Fprint(w, `{"route_supported":false,"reply_to_sender":false,"interrupt":true,"kinds_available":[],"delivery":"next-turn","runtimes":{"codex-app-server":{"route_supported":false,"reply_to_sender":false,"interrupt":true,"kinds_available":[],"final_text_confidence":"unavailable"}}}`)
			}))
			defer srv.Close()
			c := tether.MustNew(srv.URL, tether.WithToken(""))
			out, err := c.RoutingCapabilities(context.Background(), sessionID)
			if err != nil || out.RouteSupported || !out.Interrupt || out.Delivery != "next-turn" || out.Runtimes["codex-app-server"].FinalTextConfidence != "unavailable" {
				t.Fatalf("capabilities = %+v, %v", out, err)
			}
		})
	}
}
