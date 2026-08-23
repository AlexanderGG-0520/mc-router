package router

import (
	"errors"
	"fmt"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
)

var (
	ErrNoRoute              = errors.New("no route for requested server address")
	ErrInvalidServerAddress = errors.New("invalid requested server address")
)

type Router struct {
	routes            map[string]routeEntry
	defaultBackend    string
	unknownHostPolicy string
}

type routeEntry struct {
	route     config.Route
	matchKind string
}

type Selection struct {
	Backend                string
	StatusBackend          string
	MatchedBy              string
	MatchKind              string
	RouteSource            string
	CanonicalServerAddress string
	StatusOverride         *config.StatusOverride
}

const (
	MatchKindCanonical = "canonical"
	MatchKindAlias     = "alias"
	MatchKindDefault   = "default"
	RouteSourceDefault = "default"
)

func New(cfg config.Config) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	r := &Router{
		routes:            make(map[string]routeEntry, len(cfg.Routes)),
		defaultBackend:    cfg.DefaultRoute.Backend,
		unknownHostPolicy: cfg.UnknownHostPolicy,
	}
	for _, route := range cfg.Routes {
		normalized, err := hostaddr.Normalize(route.ServerAddress)
		if err != nil {
			return nil, err
		}
		route.ServerAddress = normalized
		if route.Source == "" {
			route.Source = discovery.RouteSourceStatic
		}
		route.StatusOverride = cloneStatusOverride(route.StatusOverride)
		r.routes[normalized] = routeEntry{route: route, matchKind: MatchKindCanonical}
		for _, alias := range route.Aliases {
			normalizedAlias, err := hostaddr.Normalize(alias)
			if err != nil {
				return nil, err
			}
			r.routes[normalizedAlias] = routeEntry{route: route, matchKind: MatchKindAlias}
		}
	}
	return r, nil
}

func (r *Router) Select(serverAddress string) (Selection, error) {
	address, err := hostaddr.Normalize(serverAddress)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: %w", ErrInvalidServerAddress, err)
	}
	if entry, ok := r.routes[address]; ok {
		route := entry.route
		return Selection{
			Backend:                route.Backend,
			StatusBackend:          route.StatusBackend,
			MatchedBy:              "route",
			MatchKind:              entry.matchKind,
			RouteSource:            route.Source,
			CanonicalServerAddress: route.ServerAddress,
			StatusOverride:         cloneStatusOverride(route.StatusOverride),
		}, nil
	}
	if r.unknownHostPolicy == config.UnknownHostDefault && r.defaultBackend != "" {
		return Selection{Backend: r.defaultBackend, MatchedBy: "default", MatchKind: MatchKindDefault, RouteSource: RouteSourceDefault}, nil
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
