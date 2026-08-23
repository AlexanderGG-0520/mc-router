package proxy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
)

func TestRebuildRouteSnapshot(t *testing.T) {
	cfg := config.Defaults()
	cfg.Routes = []config.Route{
		{ServerAddress: "static.example.com", Backend: "127.0.0.1:25565"},
	}
	discovered := []kubernetes.DiscoveredRoute{
		{Host: "discovered.example.com", Backend: "service1.ns1.svc.cluster.local:25565"},
	}
	provider := discovery.NewMemoryProvider(discovered)

	ctx := context.Background()
	snapshot, err := RebuildRouteSnapshot(ctx, cfg, provider)
	if err != nil {
		t.Fatalf("RebuildRouteSnapshot() error = %v", err)
	}

	// Verify both static and discovered routes are present
	wantRoutes := []config.Route{
		{ServerAddress: "discovered.example.com", Backend: "service1.ns1.svc.cluster.local:25565", Source: discovery.RouteSourceDiscovered},
		{ServerAddress: "static.example.com", Backend: "127.0.0.1:25565", Source: discovery.RouteSourceStatic},
	}
	if !reflect.DeepEqual(snapshot.Config.Routes, wantRoutes) {
		t.Errorf("snapshot.Config.Routes = %v, want %v", snapshot.Config.Routes, wantRoutes)
	}
}

func TestRebuildRouteSnapshot_ProviderError(t *testing.T) {
	cfg := config.Defaults()
	cfg.Routes = []config.Route{
		{ServerAddress: "static.example.com", Backend: "127.0.0.1:25565"},
	}
	provider := &errorProvider{err: errors.New("discovery failed")}

	ctx := context.Background()
	_, err := RebuildRouteSnapshot(ctx, cfg, provider)
	if err == nil {
		t.Fatal("RebuildRouteSnapshot() error = nil, want error")
	}
	if err.Error() != "discovery failed" {
		t.Errorf("error = %v, want 'discovery failed'", err)
	}
}

func TestRebuildRouteSnapshot_NilProvider(t *testing.T) {
	cfg := config.Defaults()
	cfg.Routes = []config.Route{
		{ServerAddress: "static.example.com", Backend: "127.0.0.1:25565"},
	}

	ctx := context.Background()
	snapshot, err := RebuildRouteSnapshot(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("RebuildRouteSnapshot() error = %v", err)
	}

	if len(snapshot.Config.Routes) != 1 || snapshot.Config.Routes[0].ServerAddress != "static.example.com" {
		t.Errorf("snapshot.Config.Routes = %v, want only static route", snapshot.Config.Routes)
	}
}

func TestRebuildRouteSnapshot_StaticWins(t *testing.T) {
	cfg := config.Defaults()
	cfg.Routes = []config.Route{
		{ServerAddress: "conflict.example.com", Backend: "static:25565"},
	}
	discovered := []kubernetes.DiscoveredRoute{
		{Host: "conflict.example.com", Backend: "discovered.ns.svc.cluster.local:25565"},
	}
	provider := discovery.NewMemoryProvider(discovered)

	ctx := context.Background()
	snapshot, err := RebuildRouteSnapshot(ctx, cfg, provider)
	if err != nil {
		t.Fatalf("RebuildRouteSnapshot() error = %v", err)
	}

	if len(snapshot.Config.Routes) != 1 || snapshot.Config.Routes[0].Backend != "static:25565" {
		t.Errorf("snapshot.Config.Routes = %v, want static backend", snapshot.Config.Routes)
	}
	if len(snapshot.DiscoveryMerge.Ignored) != 1 || snapshot.DiscoveryMerge.Ignored[0].Reason != discovery.ReasonStaticRoutePrecedence {
		t.Errorf("DiscoveryMerge.Ignored = %v, want reason static_route_precedence", snapshot.DiscoveryMerge.Ignored)
	}
}

func TestRebuildRouteSnapshotInvalidConfigDoesNotCallProvider(t *testing.T) {
	// Invalid config (empty Listen)
	cfg := config.Config{}
	provider := &panicProvider{t: t}

	ctx := context.Background()
	_, err := RebuildRouteSnapshot(ctx, cfg, provider)
	if err == nil {
		t.Fatal("RebuildRouteSnapshot() error = nil, want config validation error")
	}
}

func TestRebuildRouteSnapshotInvalidConfigTakesPrecedenceOverProviderError(t *testing.T) {
	// Invalid config (empty Listen)
	cfg := config.Config{}
	// Provider that would return error if called
	provider := &errorProvider{err: errors.New("provider error")}

	ctx := context.Background()
	_, err := RebuildRouteSnapshot(ctx, cfg, provider)
	if err == nil {
		t.Fatal("RebuildRouteSnapshot() error = nil, want config validation error")
	}
	// The error should be from config validation, not provider
	if err.Error() == "provider error" {
		t.Errorf("error = %v, want config validation error", err)
	}
}

type errorProvider struct {
	err error
}

func (p *errorProvider) Routes(ctx context.Context) ([]kubernetes.DiscoveredRoute, error) {
	return nil, p.err
}

type panicProvider struct {
	t *testing.T
}

func (p *panicProvider) Routes(ctx context.Context) ([]kubernetes.DiscoveredRoute, error) {
	p.t.Helper()
	p.t.Fatal("provider.Routes(ctx) was called for invalid config")
	return nil, nil
}
