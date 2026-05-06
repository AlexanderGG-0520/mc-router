package discovery

import (
	"context"
	"sync"

	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
)

// RouteProvider is the interface that wraps the basic Routes method.
// It returns a list of discovered routes from an external source.
type RouteProvider interface {
	Routes(ctx context.Context) ([]kubernetes.DiscoveredRoute, error)
}

// MemoryProvider is an in-memory implementation of RouteProvider.
// It is useful for testing and as a placeholder for future implementations.
type MemoryProvider struct {
	mu     sync.RWMutex
	routes []kubernetes.DiscoveredRoute
}

var _ kubernetes.RouteSink = (*MemoryProvider)(nil)

// NewMemoryProvider returns a new MemoryProvider initialized with the given routes.
func NewMemoryProvider(routes []kubernetes.DiscoveredRoute) *MemoryProvider {
	p := &MemoryProvider{}
	p.Update(routes)
	return p
}

// Routes returns a copy of the currently stored discovered routes.
func (p *MemoryProvider) Routes(ctx context.Context) ([]kubernetes.DiscoveredRoute, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneRoutes(p.routes), nil
}

// Update replaces the stored routes with a new list.
// The input slice is cloned to ensure internal state is not mutated by the caller.
func (p *MemoryProvider) Update(routes []kubernetes.DiscoveredRoute) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes = cloneRoutes(routes)
}

func cloneRoutes(routes []kubernetes.DiscoveredRoute) []kubernetes.DiscoveredRoute {
	if routes == nil {
		return nil
	}
	res := make([]kubernetes.DiscoveredRoute, len(routes))
	copy(res, routes)
	return res
}
