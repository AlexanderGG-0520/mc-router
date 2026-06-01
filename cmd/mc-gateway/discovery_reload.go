package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
)

// reloadDiscoveryReport is the command-layer boundary for a future Kubernetes
// Service re-list during SIGHUP reload. Result.Routes is converted through a
// route-only provider for application; skipped metadata stays here until a
// successful reload apply can record metrics.
type reloadDiscoveryReport struct {
	Result k8sdiscovery.Result
}

type reloadDiscoveryApplier interface {
	ReloadFile(path string) error
	ReloadConfigWithDiscovery(ctx context.Context, path string, cfg config.Config, provider discovery.RouteProvider) error
	Metrics() *gatewaymetrics.Recorder
}

type kubernetesReloadReloader struct {
	ctx              context.Context
	applier          reloadDiscoveryApplier
	logger           *slog.Logger
	deps             discoveryStartupDeps
	kubernetesConfig config.KubernetesDiscovery
}

func newKubernetesReloadReloader(ctx context.Context, applier reloadDiscoveryApplier, logger *slog.Logger, deps discoveryStartupDeps, kubernetesConfig config.KubernetesDiscovery) *kubernetesReloadReloader {
	return &kubernetesReloadReloader{
		ctx:              ctx,
		applier:          applier,
		logger:           logger,
		deps:             deps,
		kubernetesConfig: kubernetesConfig,
	}
}

func (r *kubernetesReloadReloader) ReloadFile(path string) error {
	if !r.kubernetesConfig.Enabled {
		return r.applier.ReloadFile(path)
	}

	cfg, err := config.LoadFile(path)
	if err != nil {
		r.recordReloadFailed(path, err)
		return err
	}
	discoveryConfig := cfg
	discoveryConfig.Discovery.Kubernetes = r.kubernetesConfig
	report, err := buildReloadDiscoveryReport(r.ctx, discoveryConfig, r.deps)
	if err != nil {
		r.recordReloadFailed(path, err)
		return err
	}
	if err := r.applier.ReloadConfigWithDiscovery(r.ctx, path, cfg, report.routeProvider()); err != nil {
		return err
	}
	if metrics := r.applier.Metrics(); metrics != nil {
		metrics.KubernetesSkippedServicesByReason(report.Result.SkippedByReason)
	}
	return nil
}

func (r *kubernetesReloadReloader) recordReloadFailed(path string, err error) {
	if metrics := r.applier.Metrics(); metrics != nil {
		metrics.Reload(gatewaymetrics.ReloadResultFailed)
	}
	if r.logger != nil {
		r.logger.Error("reload_failed", "config", path, "error", err)
	}
}

func newReloadDiscoveryReport(result k8sdiscovery.Result) reloadDiscoveryReport {
	return reloadDiscoveryReport{Result: cloneKubernetesDiscoveryResult(result)}
}

func (r reloadDiscoveryReport) routeProvider() discovery.RouteProvider {
	return k8sdiscovery.NewSnapshotProviderFromResult(r.Result)
}

func buildReloadDiscoveryReport(ctx context.Context, cfg config.Config, deps discoveryStartupDeps) (reloadDiscoveryReport, error) {
	deps = deps.withDefaults()
	kubernetesConfig := cfg.Discovery.Kubernetes
	namespace, err := deps.resolveNamespace(kubernetesConfig.Namespace, deps.namespaceResolver)
	if err != nil {
		return reloadDiscoveryReport{}, fmt.Errorf("resolve Kubernetes discovery namespace: %w", err)
	}
	restConfig, err := deps.inClusterConfig()
	if err != nil {
		return reloadDiscoveryReport{}, fmt.Errorf("create in-cluster Kubernetes config: %w", err)
	}
	lister, err := deps.newServiceLister(restConfig)
	if err != nil {
		return reloadDiscoveryReport{}, fmt.Errorf("create Kubernetes Service lister: %w", err)
	}
	services, err := lister.ListServices(ctx, namespace)
	if err != nil {
		return reloadDiscoveryReport{}, fmt.Errorf("list Kubernetes Services in resolved namespace: %w", err)
	}
	result := k8sdiscovery.BuildDiscoveredRoutes(services, k8sdiscovery.Options{
		AnnotationPrefix: kubernetesConfig.AnnotationPrefix,
	})
	return newReloadDiscoveryReport(result), nil
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
