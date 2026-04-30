package router

import (
	"errors"
	"testing"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
)

func TestRouterSelectsExactRoute(t *testing.T) {
	r, err := New(config.Config{
		Listen:             ":25565",
		HandshakeTimeout:   config.Duration{Duration: 1},
		BackendDialTimeout: config.Duration{Duration: 1},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: "smp.default.svc.cluster.local:25565"},
		},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	got, err := r.Select("SMP.EXAMPLE.COM.")
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if got.Backend != "smp.default.svc.cluster.local:25565" {
		t.Fatalf("backend = %q", got.Backend)
	}
	if got.MatchedBy != "route" {
		t.Fatalf("matched by = %q", got.MatchedBy)
	}
}

func TestRouterUsesDefaultRoute(t *testing.T) {
	r, err := New(config.Config{
		Listen:             ":25565",
		HandshakeTimeout:   config.Duration{Duration: 1},
		BackendDialTimeout: config.Duration{Duration: 1},
		UnknownHostPolicy:  config.UnknownHostDefault,
		DefaultRoute: config.DefaultRoute{
			Backend: "lobby.default.svc.cluster.local:25565",
			Mode:    config.RouteModeAllow,
		},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	got, err := r.Select("unknown.example.com")
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if got.Backend != "lobby.default.svc.cluster.local:25565" {
		t.Fatalf("backend = %q", got.Backend)
	}
	if got.MatchedBy != "default" {
		t.Fatalf("matched by = %q", got.MatchedBy)
	}
}

func TestRouterDeniesUnknownHost(t *testing.T) {
	r, err := New(config.Config{
		Listen:             ":25565",
		HandshakeTimeout:   config.Duration{Duration: 1},
		BackendDialTimeout: config.Duration{Duration: 1},
		UnknownHostPolicy:  config.UnknownHostDeny,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	_, err = r.Select("unknown.example.com")
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("error = %v, want %v", err, ErrNoRoute)
	}
}

func TestRouterRejectsInvalidHost(t *testing.T) {
	r, err := New(config.Config{
		Listen:             ":25565",
		HandshakeTimeout:   config.Duration{Duration: 1},
		BackendDialTimeout: config.Duration{Duration: 1},
		UnknownHostPolicy:  config.UnknownHostDeny,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	_, err = r.Select("bad host.example.com")
	if !errors.Is(err, ErrInvalidServerAddress) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidServerAddress)
	}
}
