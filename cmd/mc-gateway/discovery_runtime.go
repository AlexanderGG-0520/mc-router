package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type discoveredRouteUpdater interface {
	UpdateDiscoveredRoutes(context.Context, []k8sdiscovery.DiscoveredRoute) error
}

type serviceWatchController interface {
	Run(context.Context) error
}

type discoveryRuntimeDeps struct {
	namespaceResolver         k8sdiscovery.NamespaceResolver
	resolveNamespace          func(string, k8sdiscovery.NamespaceResolver) (string, error)
	inClusterConfig           func() (*rest.Config, error)
	newKubernetesClient       func(*rest.Config) (k8sclient.Interface, error)
	newServiceWatchController func(k8sclient.Interface, string, k8sdiscovery.RouteSink, k8sdiscovery.ServiceWatchControllerOptions) (serviceWatchController, error)
}

type runtimeDiscovery struct {
	cancel context.CancelFunc
	done   <-chan error
}

func defaultDiscoveryRuntimeDeps() discoveryRuntimeDeps {
	return discoveryRuntimeDeps{
		resolveNamespace: k8sdiscovery.ResolveNamespace,
		inClusterConfig:  rest.InClusterConfig,
		newKubernetesClient: func(restConfig *rest.Config) (k8sclient.Interface, error) {
			return k8sclient.NewForConfig(restConfig)
		},
		newServiceWatchController: func(client k8sclient.Interface, namespace string, sink k8sdiscovery.RouteSink, options k8sdiscovery.ServiceWatchControllerOptions) (serviceWatchController, error) {
			return k8sdiscovery.NewServiceWatchController(client, namespace, sink, options)
		},
	}
}

func startKubernetesRuntimeDiscovery(ctx context.Context, cfg config.Config, updater discoveredRouteUpdater, logger *slog.Logger, deps discoveryRuntimeDeps) (*runtimeDiscovery, error) {
	if !cfg.Discovery.Kubernetes.Enabled {
		return nil, nil
	}
	if updater == nil {
		return nil, errors.New("discovered route updater is nil")
	}

	deps = deps.withDefaults()
	kubernetesConfig := cfg.Discovery.Kubernetes
	namespace, err := deps.resolveNamespace(kubernetesConfig.Namespace, deps.namespaceResolver)
	if err != nil {
		return nil, fmt.Errorf("resolve Kubernetes discovery namespace: %w", err)
	}
	restConfig, err := deps.inClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("create in-cluster Kubernetes config: %w", err)
	}
	client, err := deps.newKubernetesClient(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	sink := newRuntimeDiscoverySink(childCtx, updater, logger)
	controller, err := deps.newServiceWatchController(client, namespace, sink, k8sdiscovery.ServiceWatchControllerOptions{
		AnnotationPrefix: kubernetesConfig.AnnotationPrefix,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create Kubernetes Service watch controller: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		logKubernetesRuntimeDiscoveryStarted(logger)
		err := controller.Run(childCtx)
		if err != nil && childCtx.Err() == nil {
			logKubernetesRuntimeDiscoveryFailed(logger, err)
		} else {
			logKubernetesRuntimeDiscoveryStopped(logger)
		}
		done <- err
	}()

	select {
	case <-sink.ready():
		return &runtimeDiscovery{cancel: cancel, done: done}, nil
	case err := <-done:
		cancel()
		if err == nil {
			return nil, errors.New("Kubernetes Service watch controller stopped before becoming ready")
		}
		return nil, err
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

func (r *runtimeDiscovery) Stop() {
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
}

func (d discoveryRuntimeDeps) withDefaults() discoveryRuntimeDeps {
	defaults := defaultDiscoveryRuntimeDeps()
	if d.resolveNamespace == nil {
		d.resolveNamespace = defaults.resolveNamespace
	}
	if d.inClusterConfig == nil {
		d.inClusterConfig = defaults.inClusterConfig
	}
	if d.newKubernetesClient == nil {
		d.newKubernetesClient = defaults.newKubernetesClient
	}
	if d.newServiceWatchController == nil {
		d.newServiceWatchController = defaults.newServiceWatchController
	}
	return d
}

type runtimeDiscoverySink struct {
	ctx     context.Context
	updater discoveredRouteUpdater
	logger  *slog.Logger

	readyOnce sync.Once
	readyCh   chan struct{}
}

func newRuntimeDiscoverySink(ctx context.Context, updater discoveredRouteUpdater, logger *slog.Logger) *runtimeDiscoverySink {
	return &runtimeDiscoverySink{
		ctx:     ctx,
		updater: updater,
		logger:  logger,
		readyCh: make(chan struct{}),
	}
}

func (s *runtimeDiscoverySink) Update(routes []k8sdiscovery.DiscoveredRoute) {
	s.update(routes, 0, 0)
}

func (s *runtimeDiscoverySink) UpdateResult(result k8sdiscovery.Result) {
	s.update(result.Routes, len(result.Skipped), len(result.DuplicateHosts))
}

func (s *runtimeDiscoverySink) ready() <-chan struct{} {
	return s.readyCh
}

func (s *runtimeDiscoverySink) update(routes []k8sdiscovery.DiscoveredRoute, skipped, duplicateHosts int) {
	routes = append([]k8sdiscovery.DiscoveredRoute(nil), routes...)
	err := s.updater.UpdateDiscoveredRoutes(s.ctx, routes)
	s.readyOnce.Do(func() {
		close(s.readyCh)
	})
	if err != nil {
		logKubernetesRuntimeSnapshotFailed(s.logger, err, len(routes), skipped, duplicateHosts)
		return
	}
	logKubernetesRuntimeSnapshotUpdated(s.logger, len(routes), skipped, duplicateHosts)
}

func logKubernetesRuntimeDiscoveryStarted(logger *slog.Logger) {
	if logger != nil {
		logger.Info("kubernetes_discovery_watch_started")
	}
}

func logKubernetesRuntimeDiscoveryStopped(logger *slog.Logger) {
	if logger != nil {
		logger.Info("kubernetes_discovery_watch_stopped")
	}
}

func logKubernetesRuntimeDiscoveryFailed(logger *slog.Logger, err error) {
	if logger != nil {
		logger.Error("kubernetes_discovery_watch_failed", "error", err)
	}
}

func logKubernetesRuntimeSnapshotUpdated(logger *slog.Logger, routes, skipped, duplicateHosts int) {
	if logger != nil {
		logger.Info(
			"kubernetes_discovery_runtime_snapshot_updated",
			"discovered_routes", routes,
			"skipped_services", skipped,
			"duplicate_hosts", duplicateHosts,
		)
	}
}

func logKubernetesRuntimeSnapshotFailed(logger *slog.Logger, err error, routes, skipped, duplicateHosts int) {
	if logger != nil {
		logger.Error(
			"kubernetes_discovery_runtime_snapshot_failed",
			"error", err,
			"discovered_routes", routes,
			"skipped_services", skipped,
			"duplicate_hosts", duplicateHosts,
		)
	}
}

var _ discoveredRouteUpdater = (*proxy.Server)(nil)
