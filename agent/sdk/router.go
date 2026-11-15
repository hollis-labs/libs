package sdk

import (
	"fmt"
	"strings"

	"github.com/hollis-labs/libs/llm/core/llmcontracts"
)

// Route binds an explicit name/model to a host-owned provider. There is no
// implicit retry or cross-provider failover after a call starts.
type Route struct {
	Name     string
	Model    string
	Provider llmcontracts.Provider
}

type Router struct {
	routes      map[string]Route
	defaultName string
}

func NewRouter(defaultName string, routes ...Route) (*Router, error) {
	r := &Router{routes: map[string]Route{}, defaultName: defaultName}
	for _, route := range routes {
		if strings.TrimSpace(route.Name) == "" || strings.TrimSpace(route.Model) == "" || route.Provider == nil {
			return nil, fmt.Errorf("%w: incomplete model route", ErrInvalid)
		}
		if _, exists := r.routes[route.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate route", ErrInvalid)
		}
		r.routes[route.Name] = route
	}
	if _, ok := r.routes[defaultName]; !ok {
		return nil, fmt.Errorf("%w: default model route", ErrInvalid)
	}
	return r, nil
}

func (r *Router) selectRoute(name string, tools bool) (Route, error) {
	if r == nil {
		return Route{}, fmt.Errorf("%w: router required", ErrInvalid)
	}
	if name == "" {
		name = r.defaultName
	}
	route, ok := r.routes[name]
	if !ok {
		return Route{}, fmt.Errorf("%w: model route %s", ErrUnsupported, name)
	}
	if tools && !route.Provider.Capabilities().SupportsToolCalling {
		return Route{}, fmt.Errorf("%w: route lacks tool calling", ErrUnsupported)
	}
	return route, nil
}
