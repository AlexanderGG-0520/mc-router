package kubernetes

import "context"

// SnapshotProvider exposes a fixed Kubernetes discovery snapshot as a route provider.
type SnapshotProvider struct {
	routes []DiscoveredRoute
}

// NewSnapshotProvider returns a provider initialized with the given discovered routes.
func NewSnapshotProvider(routes []DiscoveredRoute) *SnapshotProvider {
	return &SnapshotProvider{
		routes: cloneDiscoveredRoutes(routes),
	}
}

// NewSnapshotProviderFromResult returns a provider for result.Routes and ignores other Result fields.
func NewSnapshotProviderFromResult(result Result) *SnapshotProvider {
	return NewSnapshotProvider(result.Routes)
}
}

// Routes returns a copy of the stored discovered routes.
func (p *SnapshotProvider) Routes(ctx context.Context) ([]DiscoveredRoute, error) {
	return cloneDiscoveredRoutes(p.routes), nil
}

func cloneDiscoveredRoutes(routes []DiscoveredRoute) []DiscoveredRoute {
	if routes == nil {
		return nil
	}
	cloned := make([]DiscoveredRoute, len(routes))
	copy(cloned, routes)
	return cloned
}
