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
	routes            map[string]string
	defaultBackend    string
	unknownHostPolicy string
}

type Selection struct {
	Backend   string
	MatchedBy string
}

func New(cfg config.Config) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	r := &Router{
		routes:            make(map[string]string, len(cfg.Routes)),
		defaultBackend:    cfg.DefaultRoute.Backend,
		unknownHostPolicy: cfg.UnknownHostPolicy,
	}
	for _, route := range cfg.Routes {
		normalized, err := hostaddr.Normalize(route.ServerAddress)
		if err != nil {
			return nil, err
		}
		r.routes[normalized] = route.Backend
	}
	return r, nil
}

func (r *Router) Select(serverAddress string) (Selection, error) {
	address, err := hostaddr.Normalize(serverAddress)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: %w", ErrInvalidServerAddress, err)
	}
	if backend, ok := r.routes[address]; ok {
		return Selection{Backend: backend, MatchedBy: "route"}, nil
	}
	if r.unknownHostPolicy == config.UnknownHostDefault && r.defaultBackend != "" {
		return Selection{Backend: r.defaultBackend, MatchedBy: "default"}, nil
	}
	return Selection{}, fmt.Errorf("%w: %q", ErrNoRoute, address)
}
