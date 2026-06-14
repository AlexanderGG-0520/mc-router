package udprelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
)

type NamedBackendRoute struct {
	Name    string
	Backend string
}

type staticBackendResolver struct {
	backend *net.UDPAddr
}

type namedBackendResolver struct {
	defaultBackend *net.UDPAddr
	routes         map[string]*net.UDPAddr
}

func NewStaticBackendResolver(backend string) (BackendResolver, error) {
	addr, err := resolveBackend(backend)
	if err != nil {
		return nil, err
	}
	return &staticBackendResolver{backend: addr}, nil
}

func NewNamedBackendResolver(defaultBackend string, routes []NamedBackendRoute) (BackendResolver, error) {
	defaultAddr, err := resolveBackend(defaultBackend)
	if err != nil {
		return nil, err
	}

	resolvedRoutes := make(map[string]*net.UDPAddr, len(routes))
	for _, route := range routes {
		if _, ok := resolvedRoutes[route.Name]; ok {
			return nil, fmt.Errorf("duplicate UDP backend route %q", route.Name)
		}
		addr, err := resolveBackend(route.Backend)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.Name, err)
		}
		resolvedRoutes[route.Name] = addr
	}

	return &namedBackendResolver{
		defaultBackend: defaultAddr,
		routes:         resolvedRoutes,
	}, nil
}

func (r *staticBackendResolver) ResolveBackend(context.Context, *net.UDPAddr, []byte) (BackendSelection, error) {
	return BackendSelection{Name: "default", Addr: cloneUDPAddr(r.backend)}, nil
}

func (r *staticBackendResolver) DefaultBackend() string {
	if r == nil || r.backend == nil {
		return ""
	}
	return r.backend.String()
}

func (r *namedBackendResolver) ResolveBackend(context.Context, *net.UDPAddr, []byte) (BackendSelection, error) {
	if r == nil || r.defaultBackend == nil {
		return BackendSelection{}, errors.New("default backend is not configured")
	}
	return BackendSelection{Name: "default", Addr: cloneUDPAddr(r.defaultBackend)}, nil
}

func (r *namedBackendResolver) DefaultBackend() string {
	if r == nil || r.defaultBackend == nil {
		return ""
	}
	return r.defaultBackend.String()
}

func (r *namedBackendResolver) RouteNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.routes))
	for name := range r.routes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func resolveBackend(backend string) (*net.UDPAddr, error) {
	addr, err := net.ResolveUDPAddr("udp", backend)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP backend address %q: %w", backend, err)
	}
	if err := validateBackendAddr(addr); err != nil {
		return nil, fmt.Errorf("invalid UDP backend address %q: %w", backend, err)
	}
	return addr, nil
}
