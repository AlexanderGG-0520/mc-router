package bedrockroute

import "fmt"

type Route struct {
	Name    string
	Hosts   []string
	Backend string
}

type Selection struct {
	RouteName string
	Backend   string
	Matched   bool
}

type Router struct {
	defaultBackend string
	hosts          map[string]Selection
}

func NewRouter(defaultBackend string, routes []Route) (*Router, error) {
	hosts := make(map[string]Selection)
	for _, route := range routes {
		for _, rawHost := range route.Hosts {
			host, err := NormalizeHost(rawHost)
			if err != nil {
				return nil, fmt.Errorf("route %q host %q: %w", route.Name, rawHost, err)
			}
			if existing, ok := hosts[host]; ok {
				return nil, fmt.Errorf("host %q duplicates route %q", host, existing.RouteName)
			}
			hosts[host] = Selection{
				RouteName: route.Name,
				Backend:   route.Backend,
				Matched:   true,
			}
		}
	}
	return &Router{defaultBackend: defaultBackend, hosts: hosts}, nil
}

func (r *Router) Select(requestedHost string) Selection {
	if r == nil {
		return Selection{}
	}
	if host, err := NormalizeHost(requestedHost); err == nil {
		if selection, ok := r.hosts[host]; ok {
			return selection
		}
	}
	return Selection{
		RouteName: "default",
		Backend:   r.defaultBackend,
		Matched:   false,
	}
}
