package tether

import (
	"context"
	"net/url"
)

// RuntimeRoutingCapabilities describes the currently wired runtime path.
// Availability may change with daemon upgrades; callers should discover it.
type RuntimeRoutingCapabilities struct {
	RouteSupported      bool     `json:"route_supported"`
	ReplyToSender       bool     `json:"reply_to_sender"`
	Interrupt           bool     `json:"interrupt"`
	KindsAvailable      []string `json:"kinds_available"`
	FinalTextConfidence string   `json:"final_text_confidence"`
}

type RoutingCapabilitiesResponse struct {
	RouteSupported bool                                  `json:"route_supported"`
	ReplyToSender  bool                                  `json:"reply_to_sender"`
	Interrupt      bool                                  `json:"interrupt"`
	KindsAvailable []string                              `json:"kinds_available"`
	Delivery       string                                `json:"delivery"`
	Runtimes       map[string]RuntimeRoutingCapabilities `json:"runtimes"`
	SessionID      string                                `json:"session_id,omitempty"`
}

// RoutingCapabilities returns gateway-wide capabilities when sessionID is
// empty, or the capabilities of the specified session's runtime otherwise.
func (c *Client) RoutingCapabilities(ctx context.Context, sessionID string) (RoutingCapabilitiesResponse, error) {
	q := make(url.Values)
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	var out RoutingCapabilitiesResponse
	err := c.getJSON(ctx, withQuery("/routing/capabilities", q), &out)
	return out, err
}
