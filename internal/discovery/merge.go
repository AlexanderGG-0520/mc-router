package discovery

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
)

const (
	ReasonStaticRoutePrecedence = "static_route_precedence"
	ReasonDuplicateDiscovered   = "duplicate_discovered_host"
	ReasonInvalidDiscovered     = "invalid_discovered_route"
	ReasonUnknown               = "unknown"
)

type MergeOptions struct {
	DefaultRoute config.DefaultRoute
}

type MergeResult struct {
	Routes  []config.Route
	Ignored []IgnoredDiscoveredRoute
	Stats   MergeStats
}

type MergeStats struct {
	StaticRoutes            int
	DiscoveredRoutes        int
	MergedRoutes            int
	IgnoredDiscoveredRoutes int
	HasDefaultRoute         bool
	IgnoredByReason         map[string]int
}

type IgnoredDiscoveredRoute struct {
	Host    string
	Backend string
	Reason  string
	Err     error
}

type discoveredCandidate struct {
	Host    string
	Backend string
}

func MergeRoutes(staticRoutes []config.Route, discovered []kubernetes.DiscoveredRoute, opts MergeOptions) MergeResult {
	result := MergeResult{
		Routes:  make([]config.Route, 0, len(staticRoutes)+len(discovered)),
		Ignored: make([]IgnoredDiscoveredRoute, 0),
		Stats: MergeStats{
			StaticRoutes:     len(staticRoutes),
			DiscoveredRoutes: len(discovered),
			HasDefaultRoute:  strings.TrimSpace(opts.DefaultRoute.Backend) != "",
			IgnoredByReason:  make(map[string]int),
		},
	}

	staticHosts := make(map[string]struct{}, len(staticRoutes))
	for _, route := range staticRoutes {
		normalized := route
		if host, err := hostaddr.Normalize(route.ServerAddress); err == nil {
			normalized.ServerAddress = host
			staticHosts[host] = struct{}{}
		}
		result.Routes = append(result.Routes, normalized)
	}
	sort.SliceStable(result.Routes, func(i, j int) bool {
		return lessConfigRoute(result.Routes[i], result.Routes[j])
	})

	candidates := make([]discoveredCandidate, 0, len(discovered))
	hostCounts := make(map[string]int, len(discovered))
	for _, route := range discovered {
		candidate, err := validateDiscoveredRoute(route)
		if err != nil {
			result.addIgnored(IgnoredDiscoveredRoute{
				Host:    route.Host,
				Backend: route.Backend,
				Reason:  ReasonInvalidDiscovered,
				Err:     err,
			})
			continue
		}
		candidates = append(candidates, candidate)
		hostCounts[candidate.Host]++
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return lessDiscoveredCandidate(candidates[i], candidates[j])
	})

	for _, candidate := range candidates {
		if hostCounts[candidate.Host] > 1 {
			result.addIgnored(IgnoredDiscoveredRoute{
				Host:    candidate.Host,
				Backend: candidate.Backend,
				Reason:  ReasonDuplicateDiscovered,
				Err:     fmt.Errorf("host %q is discovered more than once", candidate.Host),
			})
			continue
		}
		if _, ok := staticHosts[candidate.Host]; ok {
			result.addIgnored(IgnoredDiscoveredRoute{
				Host:    candidate.Host,
				Backend: candidate.Backend,
				Reason:  ReasonStaticRoutePrecedence,
				Err:     fmt.Errorf("host %q is already configured by a static route", candidate.Host),
			})
			continue
		}
		result.Routes = append(result.Routes, config.Route{
			ServerAddress: candidate.Host,
			Backend:       candidate.Backend,
		})
	}

	sort.SliceStable(result.Routes, func(i, j int) bool {
		return lessConfigRoute(result.Routes[i], result.Routes[j])
	})
	sort.SliceStable(result.Ignored, func(i, j int) bool {
		return lessIgnored(result.Ignored[i], result.Ignored[j])
	})
	result.Stats.MergedRoutes = len(result.Routes)
	result.Stats.IgnoredDiscoveredRoutes = len(result.Ignored)
	return result
}

func (r *MergeResult) addIgnored(ignored IgnoredDiscoveredRoute) {
	if ignored.Reason == "" {
		ignored.Reason = ReasonUnknown
	}
	r.Ignored = append(r.Ignored, ignored)
	r.Stats.IgnoredByReason[ignored.Reason]++
}

func validateDiscoveredRoute(route kubernetes.DiscoveredRoute) (discoveredCandidate, error) {
	host, err := hostaddr.Normalize(route.Host)
	if err != nil {
		return discoveredCandidate{}, fmt.Errorf("host: %w", err)
	}
	if strings.TrimSpace(route.Backend) == "" {
		return discoveredCandidate{}, errors.New("backend must not be empty")
	}
	backendHost, port, err := net.SplitHostPort(route.Backend)
	if err != nil {
		return discoveredCandidate{}, fmt.Errorf("backend must be host:port: %w", err)
	}
	if err := validateBackendPort(port); err != nil {
		return discoveredCandidate{}, fmt.Errorf("backend port: %w", err)
	}
	normalizedBackendHost, err := hostaddr.Normalize(backendHost)
	if err != nil {
		return discoveredCandidate{}, fmt.Errorf("backend host: %w", err)
	}
	if err := validateServiceDNSBackend(normalizedBackendHost); err != nil {
		return discoveredCandidate{}, fmt.Errorf("backend host: %w", err)
	}
	return discoveredCandidate{
		Host:    host,
		Backend: net.JoinHostPort(normalizedBackendHost, port),
	}, nil
}

func validateBackendPort(port string) error {
	if strings.TrimSpace(port) == "" {
		return errors.New("must not be empty")
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return errors.New("must be an integer from 1 to 65535")
	}
	return nil
}

func validateServiceDNSBackend(host string) error {
	const suffix = ".svc.cluster.local"
	if !strings.HasSuffix(host, suffix) {
		return fmt.Errorf("must end with %q", strings.TrimPrefix(suffix, "."))
	}
	prefix := strings.TrimSuffix(host, suffix)
	parts := strings.Split(prefix, ".")
	if len(parts) != 2 {
		return errors.New("must be service.namespace.svc.cluster.local")
	}
	for _, part := range parts {
		if !isDNSLabel(part) {
			return errors.New("service and namespace must be DNS labels")
		}
	}
	return nil
}

func isDNSLabel(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		valid := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !valid {
			return false
		}
	}
	return value[0] != '-' && value[len(value)-1] != '-'
}

func lessConfigRoute(left, right config.Route) bool {
	if left.ServerAddress != right.ServerAddress {
		return left.ServerAddress < right.ServerAddress
	}
	return left.Backend < right.Backend
}

func lessDiscoveredCandidate(left, right discoveredCandidate) bool {
	if left.Host != right.Host {
		return left.Host < right.Host
	}
	return left.Backend < right.Backend
}

func lessIgnored(left, right IgnoredDiscoveredRoute) bool {
	if left.Reason != right.Reason {
		return left.Reason < right.Reason
	}
	if left.Host != right.Host {
		return left.Host < right.Host
	}
	if left.Backend != right.Backend {
		return left.Backend < right.Backend
	}
	return errorString(left.Err) < errorString(right.Err)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
