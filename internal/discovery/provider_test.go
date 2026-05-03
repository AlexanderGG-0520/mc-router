package discovery

import (
	"context"
	"reflect"
	"testing"

	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
)

func TestMemoryProvider(t *testing.T) {
	initialRoutes := []kubernetes.DiscoveredRoute{
		{Host: "mc.example.com", Backend: "service1.ns1.svc.cluster.local:25565"},
	}
	p := NewMemoryProvider(initialRoutes)

	ctx := context.Background()
	routes, err := p.Routes(ctx)
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}

	if !reflect.DeepEqual(routes, initialRoutes) {
		t.Errorf("Routes() = %v, want %v", routes, initialRoutes)
	}

	// Test cloning - mutation of returned slice should not affect provider
	routes[0].Host = "mutated.example.com"
	routes2, _ := p.Routes(ctx)
	if reflect.DeepEqual(routes2, routes) {
		t.Errorf("Routes() returned slice was mutated by caller")
	}

	// Test Update cloning
	newRoutes := []kubernetes.DiscoveredRoute{
		{Host: "mc2.example.com", Backend: "service2.ns2.svc.cluster.local:25565"},
	}
	p.Update(newRoutes)
	newRoutes[0].Host = "mutated-input.example.com"

	routes3, _ := p.Routes(ctx)
	if reflect.DeepEqual(routes3, newRoutes) {
		t.Errorf("Update() stored slice was mutated by caller")
	}

	expectedAfterUpdate := []kubernetes.DiscoveredRoute{
		{Host: "mc2.example.com", Backend: "service2.ns2.svc.cluster.local:25565"},
	}
	if !reflect.DeepEqual(routes3, expectedAfterUpdate) {
		t.Errorf("Routes() after update = %v, want %v", routes3, expectedAfterUpdate)
	}
}

func TestMemoryProvider_Nil(t *testing.T) {
	p := NewMemoryProvider(nil)
	routes, err := p.Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}
	if routes != nil {
		t.Errorf("Routes() = %v, want nil", routes)
	}

	p.Update([]kubernetes.DiscoveredRoute{{Host: "h", Backend: "b:1"}})
	routes, _ = p.Routes(context.Background())
	if len(routes) != 1 {
		t.Errorf("len(Routes()) = %d, want 1", len(routes))
	}

	p.Update(nil)
	routes, _ = p.Routes(context.Background())
	if routes != nil {
		t.Errorf("Routes() = %v, want nil", routes)
	}
}
