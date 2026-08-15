package router

import (
	"errors"
	"fmt"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
)

var (
	ErrNoRoute              = errors.New("no route for requested server address")
	ErrInvalidServerAddress = errors.New("invalid requested server address")
)

type Router struct {
	routes            map[string]config.Route
	defaultBackend    string
	unknownHostPolicy string
}

type Selection struct {
	Backend        string
	MatchedBy      string
	StatusOverride *config.StatusOverride
}

func New(cfg config.Config) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	r := &Router{
		routes:            make(map[string]config.Route, len(cfg.Routes)),
		defaultBackend:    cfg.DefaultRoute.Backend,
		unknownHostPolicy: cfg.UnknownHostPolicy,
	}
	for _, route := range cfg.Routes {
		normalized, err := hostaddr.Normalize(route.ServerAddress)
		if err != nil {
			return nil, err
		}
		route.ServerAddress = normalized
		route.StatusOverride = cloneStatusOverride(route.StatusOverride)
		r.routes[normalized] = route
	}
	return r, nil
}

func (r *Router) Select(serverAddress string) (Selection, error) {
	address, err := hostaddr.Normalize(serverAddress)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: %w", ErrInvalidServerAddress, err)
	}
	if route, ok := r.routes[address]; ok {
		return Selection{
			Backend:        route.Backend,
			MatchedBy:      "route",
			StatusOverride: cloneStatusOverride(route.StatusOverride),
		}, nil
	}
	if r.unknownHostPolicy == config.UnknownHostDefault && r.defaultBackend != "" {
		return Selection{Backend: r.defaultBackend, MatchedBy: "default"}, nil
	}
	return Selection{}, fmt.Errorf("%w: %q", ErrNoRoute, address)
}

func cloneStatusOverride(override *config.StatusOverride) *config.StatusOverride {
	if override == nil {
		return nil
	}
	cloned := *override
	return &cloned
}
