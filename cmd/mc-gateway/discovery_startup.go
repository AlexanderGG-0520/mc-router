package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type discoveryStartupDeps struct {
	namespaceResolver k8sdiscovery.NamespaceResolver
	resolveNamespace  func(string, k8sdiscovery.NamespaceResolver) (string, error)
	inClusterConfig   func() (*rest.Config, error)
	newServiceLister  func(*rest.Config) (k8sdiscovery.ServiceLister, error)
}

// startupDiscoveryReport carries startup discovery metadata for logging and
// future startup metrics. Result.Routes is converted through SnapshotProvider
// for route merging; skipped metadata must not flow through RouteProvider or
// RouteSnapshot.
type startupDiscoveryReport struct {
	Services []k8sdiscovery.ServiceInput
	Result   k8sdiscovery.Result
}

func defaultDiscoveryStartupDeps() discoveryStartupDeps {
	return discoveryStartupDeps{
		resolveNamespace: k8sdiscovery.ResolveNamespace,
		inClusterConfig:  rest.InClusterConfig,
		newServiceLister: func(restConfig *rest.Config) (k8sdiscovery.ServiceLister, error) {
			client, err := k8sclient.NewForConfig(restConfig)
			if err != nil {
				return nil, err
			}
			return k8sdiscovery.NewClientServiceLister(client), nil
		},
	}
}

func loadRouteSnapshot(ctx context.Context, configPath string, logger *slog.Logger) (proxy.RouteSnapshot, error) {
	return loadRouteSnapshotWithDiscovery(ctx, configPath, logger, defaultDiscoveryStartupDeps())
}

func loadRouteSnapshotWithDiscovery(ctx context.Context, configPath string, logger *slog.Logger, deps discoveryStartupDeps) (proxy.RouteSnapshot, error) {
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		return proxy.RouteSnapshot{}, err
	}

	if !cfg.Discovery.Kubernetes.Enabled {
		return proxy.RebuildRouteSnapshot(ctx, cfg, nil)
	}

	deps = deps.withDefaults()
	report, err := buildStartupDiscoveryReport(ctx, cfg, deps)
	if err != nil {
		return proxy.RouteSnapshot{}, err
	}
	provider := k8sdiscovery.NewSnapshotProviderFromResult(report.Result)
	snapshot, err := proxy.RebuildRouteSnapshot(ctx, cfg, provider)
	if err != nil {
		return proxy.RouteSnapshot{}, err
	}
	logInitialList(logger, len(report.Services), report.Result)
	return snapshot, nil
}

func (d discoveryStartupDeps) withDefaults() discoveryStartupDeps {
	defaults := defaultDiscoveryStartupDeps()
	if d.resolveNamespace == nil {
		d.resolveNamespace = defaults.resolveNamespace
	}
	if d.inClusterConfig == nil {
		d.inClusterConfig = defaults.inClusterConfig
	}
	if d.newServiceLister == nil {
		d.newServiceLister = defaults.newServiceLister
	}
	return d
}

func buildStartupDiscoveryReport(ctx context.Context, cfg config.Config, deps discoveryStartupDeps) (startupDiscoveryReport, error) {
	kubernetesConfig := cfg.Discovery.Kubernetes
	namespace, err := deps.resolveNamespace(kubernetesConfig.Namespace, deps.namespaceResolver)
	if err != nil {
		return startupDiscoveryReport{}, fmt.Errorf("resolve Kubernetes discovery namespace: %w", err)
	}
	restConfig, err := deps.inClusterConfig()
	if err != nil {
		return startupDiscoveryReport{}, fmt.Errorf("create in-cluster Kubernetes config: %w", err)
	}
	lister, err := deps.newServiceLister(restConfig)
	if err != nil {
		return startupDiscoveryReport{}, fmt.Errorf("create Kubernetes Service lister: %w", err)
	}
	services, err := lister.ListServices(ctx, namespace)
	if err != nil {
		return startupDiscoveryReport{}, fmt.Errorf("list Kubernetes Services in resolved namespace: %w", err)
	}
	result := k8sdiscovery.BuildDiscoveredRoutes(services, k8sdiscovery.Options{
		AnnotationPrefix: kubernetesConfig.AnnotationPrefix,
	})
	return startupDiscoveryReport{
		Services: services,
		Result:   result,
	}, nil
}

func logInitialList(logger *slog.Logger, serviceCount int, result k8sdiscovery.Result) {
	if logger == nil {
		return
	}
	logger.Info(
		"kubernetes_discovery_initial_list_success",
		"services", serviceCount,
		"discovered_routes", len(result.Routes),
		"skipped_services", len(result.Skipped),
		"duplicate_hosts", len(result.DuplicateHosts),
	)
}
