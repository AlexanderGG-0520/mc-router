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
	if got.MatchKind != MatchKindCanonical || got.RouteSource != "static" || got.CanonicalServerAddress != "smp.example.com" {
		t.Fatalf("selection provenance = %#v", got)
	}
}

func TestRouterPreservesDiscoveredRouteSource(t *testing.T) {
	r, err := New(config.Config{
		Listen:             ":25565",
		HandshakeTimeout:   config.Duration{Duration: 1},
		BackendDialTimeout: config.Duration{Duration: 1},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{{
			ServerAddress: "discovered.example.com",
			Backend:       "smp.default.svc.cluster.local:25565",
			Source:        "discovered",
		}},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	got, err := r.Select("discovered.example.com")
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if got.RouteSource != "discovered" || got.MatchKind != MatchKindCanonical {
		t.Fatalf("selection provenance = %#v", got)
	}
}

func TestRouterSelectsExplicitAlias(t *testing.T) {
	r, err := New(config.Config{
		Listen:             ":25565",
		HandshakeTimeout:   config.Duration{Duration: 1},
		BackendDialTimeout: config.Duration{Duration: 1},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{{
			ServerAddress: "127.0.0.1",
			Aliases:       []string{"localhost"},
			Backend:       "smp.default.svc.cluster.local:25565",
			StatusBackend: "status.default.svc.cluster.local:25565",
		}},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	got, err := r.Select("LOCALHOST.")
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if got.Backend != "smp.default.svc.cluster.local:25565" || got.StatusBackend != "status.default.svc.cluster.local:25565" {
		t.Fatalf("selection = %#v", got)
	}
	if got.MatchKind != MatchKindAlias || got.RouteSource != "static" || got.CanonicalServerAddress != "127.0.0.1" {
		t.Fatalf("selection provenance = %#v", got)
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
	if got.MatchKind != MatchKindDefault || got.RouteSource != RouteSourceDefault {
		t.Fatalf("selection provenance = %#v", got)
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

func TestRouterSelectsRouteStatusOverride(t *testing.T) {
	r, err := New(config.Config{
		Listen:             ":25565",
		HandshakeTimeout:   config.Duration{Duration: 1},
		BackendDialTimeout: config.Duration{Duration: 1},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{
				ServerAddress: "smp.example.com",
				Backend:       "smp.default.svc.cluster.local:25565",
				StatusBackend: "status.default.svc.cluster.local:25565",
				StatusOverride: &config.StatusOverride{
					MOTD:            "Alec SMP",
					ProtocolName:    "Alec SMP 2",
					ProtocolVersion: 767,
					MaxPlayers:      100,
					OnlinePlayers:   12,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	got, err := r.Select("SMP.EXAMPLE.COM.")
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if got.StatusOverride == nil {
		t.Fatal("status override is nil")
	}
	if got.StatusOverride.MOTD != "Alec SMP" {
		t.Fatalf("status override motd = %q", got.StatusOverride.MOTD)
	}
	if got.StatusBackend != "status.default.svc.cluster.local:25565" {
		t.Fatalf("status backend = %q", got.StatusBackend)
	}

	got.StatusOverride.MOTD = "mutated"
	again, err := r.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if again.StatusOverride.MOTD != "Alec SMP" {
		t.Fatalf("status override was mutated through selection: %q", again.StatusOverride.MOTD)
	}
}
