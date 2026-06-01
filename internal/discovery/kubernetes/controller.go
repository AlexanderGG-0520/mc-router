package kubernetes

import (
	"fmt"
	"sort"
)

type Options struct {
	AnnotationPrefix string
}

type Result struct {
	Routes          []DiscoveredRoute
	Skipped         []SkippedResource
	DuplicateHosts  []string
	SkippedByReason map[string]int
}

type SkippedResource struct {
	ServiceName string
	Namespace   string
	Host        string
	Backend     string
	Reason      string
	Err         error
}

type routeCandidate struct {
	Route       DiscoveredRoute
	ServiceName string
	Namespace   string
}

// BuildDiscoveredRoutes is the pure snapshot-construction boundary for Service
// annotation discovery. It converts a complete ServiceInput set into one
// complete discovered-route Result without calling the Kubernetes API.
func BuildDiscoveredRoutes(services []ServiceInput, options Options) Result {
	prefix := options.AnnotationPrefix
	if prefix == "" {
		prefix = DefaultAnnotationPrefix
	}

	result := Result{
		Routes:          make([]DiscoveredRoute, 0, len(services)),
		Skipped:         make([]SkippedResource, 0),
		SkippedByReason: make(map[string]int),
	}
	candidates := make([]routeCandidate, 0, len(services))

	for _, service := range services {
		parsed := safeParseServiceAnnotations(prefix, service)
		if parsed.Skipped {
			result.addSkipped(SkippedResource{
				ServiceName: service.Name,
				Namespace:   service.Namespace,
				Reason:      parsed.Reason,
				Err:         parsed.Err,
			})
			continue
		}
		candidates = append(candidates, routeCandidate{
			Route:       parsed.Route,
			ServiceName: service.Name,
			Namespace:   service.Namespace,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return lessCandidate(candidates[i], candidates[j])
	})

	hosts := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		hosts[candidate.Route.Host]++
	}

	duplicateHosts := make(map[string]struct{})
	for _, candidate := range candidates {
		if hosts[candidate.Route.Host] > 1 {
			duplicateHosts[candidate.Route.Host] = struct{}{}
			result.addSkipped(SkippedResource{
				ServiceName: candidate.ServiceName,
				Namespace:   candidate.Namespace,
				Host:        candidate.Route.Host,
				Backend:     candidate.Route.Backend,
				Reason:      ReasonDuplicateHost,
				Err:         fmt.Errorf("host %q is discovered more than once", candidate.Route.Host),
			})
			continue
		}
		result.Routes = append(result.Routes, candidate.Route)
	}

	result.DuplicateHosts = sortedKeys(duplicateHosts)
	sort.SliceStable(result.Skipped, func(i, j int) bool {
		return lessSkipped(result.Skipped[i], result.Skipped[j])
	})
	return result
}

func safeParseServiceAnnotations(prefix string, service ServiceInput) (result ParseResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = skip(ReasonUnknown, fmt.Errorf("parse service annotations panic: %v", recovered))
		}
	}()
	return ParseServiceAnnotations(prefix, service)
}

func (r *Result) addSkipped(skipped SkippedResource) {
	if skipped.Reason == "" {
		skipped.Reason = ReasonUnknown
	}
	r.Skipped = append(r.Skipped, skipped)
	r.SkippedByReason[skipped.Reason]++
}

func lessCandidate(left, right routeCandidate) bool {
	if left.Route.Host != right.Route.Host {
		return left.Route.Host < right.Route.Host
	}
	if left.Route.Backend != right.Route.Backend {
		return left.Route.Backend < right.Route.Backend
	}
	if left.Namespace != right.Namespace {
		return left.Namespace < right.Namespace
	}
	return left.ServiceName < right.ServiceName
}

func lessSkipped(left, right SkippedResource) bool {
	if left.Reason != right.Reason {
		return left.Reason < right.Reason
	}
	if left.Host != right.Host {
		return left.Host < right.Host
	}
	if left.Namespace != right.Namespace {
		return left.Namespace < right.Namespace
	}
	if left.ServiceName != right.ServiceName {
		return left.ServiceName < right.ServiceName
	}
	if left.Backend != right.Backend {
		return left.Backend < right.Backend
	}
	return errorString(left.Err) < errorString(right.Err)
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
