package kubernetes_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
)

var _ discovery.RouteProvider = (*kubernetes.SnapshotProvider)(nil)

func TestSnapshotProviderReturnsProvidedRoutes(t *testing.T) {
	want := []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
		{Host: "build.example.com", Backend: "build.minecraft.svc.cluster.local:25565"},
	}
	provider := kubernetes.NewSnapshotProvider(want)

	got, err := provider.Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes() = %v, want %v", got, want)
	}
}

func TestSnapshotProviderReturnsDefensiveCopies(t *testing.T) {
	input := []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	}
	provider := kubernetes.NewSnapshotProvider(input)
	input[0].Backend = "mutated.minecraft.svc.cluster.local:25565"

	first, err := provider.Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}
	if first[0].Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("stored backend = %q, want original backend", first[0].Backend)
	}

	first[0].Backend = "mutated-again.minecraft.svc.cluster.local:25565"
	second, err := provider.Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}
	if second[0].Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("returned route mutation affected provider: %q", second[0].Backend)
	}
}

func TestSnapshotProviderNilAndEmptyRoutes(t *testing.T) {
	nilProvider := kubernetes.NewSnapshotProvider(nil)
	routes, err := nilProvider.Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes() nil provider error = %v", err)
	}
	if routes != nil {
		t.Fatalf("Routes() nil provider = %v, want nil", routes)
	}

	emptyProvider := kubernetes.NewSnapshotProvider([]kubernetes.DiscoveredRoute{})
	routes, err = emptyProvider.Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes() empty provider error = %v", err)
	}
	if routes == nil {
		t.Fatal("Routes() empty provider = nil, want empty slice")
	}
	if len(routes) != 0 {
		t.Fatalf("len(Routes() empty provider) = %d, want 0", len(routes))
	}
}

func TestSnapshotProviderCanceledContextMatchesMemoryProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	want := []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	}
	provider := kubernetes.NewSnapshotProvider(want)

	got, err := provider.Routes(ctx)
	if err != nil {
		t.Fatalf("Routes() canceled context error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes() canceled context = %v, want %v", got, want)
	}
}
