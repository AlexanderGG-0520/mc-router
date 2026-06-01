package main

import (
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
)

// reloadDiscoveryReport is the command-layer boundary for a future Kubernetes
// Service re-list during SIGHUP reload. Result.Routes is converted through a
// route-only provider for application; skipped metadata stays here until a
// successful reload apply can record metrics.
type reloadDiscoveryReport struct {
	Result k8sdiscovery.Result
}

func newReloadDiscoveryReport(result k8sdiscovery.Result) reloadDiscoveryReport {
	return reloadDiscoveryReport{Result: cloneKubernetesDiscoveryResult(result)}
}

func (r reloadDiscoveryReport) routeProvider() discovery.RouteProvider {
	return k8sdiscovery.NewSnapshotProviderFromResult(r.Result)
}

func cloneKubernetesDiscoveryResult(result k8sdiscovery.Result) k8sdiscovery.Result {
	clone := k8sdiscovery.Result{
		Routes:          append([]k8sdiscovery.DiscoveredRoute(nil), result.Routes...),
		Skipped:         append([]k8sdiscovery.SkippedResource(nil), result.Skipped...),
		DuplicateHosts:  append([]string(nil), result.DuplicateHosts...),
		SkippedByReason: cloneStringIntMap(result.SkippedByReason),
	}
	return clone
}

func cloneStringIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	clone := make(map[string]int, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
