package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type discoveredRouteUpdater interface {
	UpdateDiscoveredRoutes(context.Context, discovery.RouteProvider) error
}

type serviceWatchController interface {
	Run(context.Context) error
}

type discoveryMetricsProvider interface {
	Metrics() *gatewaymetrics.Recorder
}

type discoveryRuntimeDeps struct {
	namespaceResolver         k8sdiscovery.NamespaceResolver
	resolveNamespace          func(string, k8sdiscovery.NamespaceResolver) (string, error)
	inClusterConfig           func() (*rest.Config, error)
	newKubernetesClient       func(*rest.Config) (k8sclient.Interface, error)
	newServiceWatchController func(k8sclient.Interface, string, k8sdiscovery.RouteSink, k8sdiscovery.ServiceWatchControllerOptions) (serviceWatchController, error)
	newWatchSupervisor        func(k8sdiscovery.WatchSupervisorOptions) (serviceWatchController, error)
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
		newWatchSupervisor: func(options k8sdiscovery.WatchSupervisorOptions) (serviceWatchController, error) {
			return k8sdiscovery.NewWatchSupervisor(options)
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
	metrics := discoveryMetrics(updater)
	kubernetesConfig := cfg.Discovery.Kubernetes
	namespace, err := deps.resolveNamespace(kubernetesConfig.Namespace, deps.namespaceResolver)
	if err != nil {
		metrics.KubernetesDiscoveryError(gatewaymetrics.KubernetesDiscoveryErrorReasonNamespaceResolutionFailed)
		return nil, fmt.Errorf("resolve Kubernetes discovery namespace: %w", err)
	}
	restConfig, err := deps.inClusterConfig()
	if err != nil {
		metrics.KubernetesDiscoveryError(gatewaymetrics.KubernetesDiscoveryErrorReasonInClusterConfigFailed)
		return nil, fmt.Errorf("create in-cluster Kubernetes config: %w", err)
	}
	client, err := deps.newKubernetesClient(restConfig)
	if err != nil {
		metrics.KubernetesDiscoveryError(gatewaymetrics.KubernetesDiscoveryErrorReasonUnknown)
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
	runner, err := deps.newWatchSupervisor(k8sdiscovery.WatchSupervisorOptions{
		Runner: controller,
		Ready:  sink.ready(),
		Synced: sink.synced(),
		Logger: logger,
		OnRetry: func(err error) {
			metrics.KubernetesWatchRestart(kubernetesWatchRestartReason(err))
			metrics.KubernetesDiscoveryError(kubernetesDiscoveryErrorReason(err))
		},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create Kubernetes Service watch supervisor: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		logKubernetesRuntimeDiscoveryStarted(logger)
		metrics.KubernetesWatchRunning(true)
		err := runner.Run(childCtx)
		metrics.KubernetesWatchRunning(false)
		if err != nil && childCtx.Err() == nil {
			metrics.KubernetesDiscoveryError(kubernetesDiscoveryErrorReason(err))
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
	if d.newWatchSupervisor == nil {
		d.newWatchSupervisor = defaults.newWatchSupervisor
	}
	return d
}

func discoveryMetrics(updater discoveredRouteUpdater) *gatewaymetrics.Recorder {
	if provider, ok := updater.(discoveryMetricsProvider); ok {
		if recorder := provider.Metrics(); recorder != nil {
			return recorder
		}
	}
	return gatewaymetrics.NewRecorder(false)
}

func kubernetesWatchRestartReason(err error) string {
	switch {
	case errors.Is(err, k8sdiscovery.ErrServiceListFailed):
		return gatewaymetrics.KubernetesWatchRestartReasonListFailed
	case errors.Is(err, k8sdiscovery.ErrServiceWatchClosed):
		return gatewaymetrics.KubernetesWatchRestartReasonWatchClosed
	case errors.Is(err, k8sdiscovery.ErrServiceWatchError):
		return gatewaymetrics.KubernetesWatchRestartReasonWatchError
	case errors.Is(err, k8sdiscovery.ErrServiceWatchSetupFailed):
		return gatewaymetrics.KubernetesWatchRestartReasonWatchSetupFailed
	default:
		return gatewaymetrics.KubernetesWatchRestartReasonUnknown
	}
}

func kubernetesDiscoveryErrorReason(err error) string {
	switch {
	case errors.Is(err, k8sdiscovery.ErrServiceListFailed):
		return gatewaymetrics.KubernetesDiscoveryErrorReasonInitialListFailed
	case errors.Is(err, k8sdiscovery.ErrServiceWatchClosed):
		return gatewaymetrics.KubernetesDiscoveryErrorReasonWatchClosed
	case errors.Is(err, k8sdiscovery.ErrServiceWatchError):
		return gatewaymetrics.KubernetesDiscoveryErrorReasonWatchError
	case errors.Is(err, k8sdiscovery.ErrServiceWatchSetupFailed):
		return gatewaymetrics.KubernetesDiscoveryErrorReasonWatchSetupFailed
	default:
		return gatewaymetrics.KubernetesDiscoveryErrorReasonUnknown
	}
}

type runtimeDiscoverySink struct {
	ctx     context.Context
	updater discoveredRouteUpdater
	logger  *slog.Logger

	readyOnce sync.Once
	readyCh   chan struct{}
	syncedCh  chan struct{}
}

func newRuntimeDiscoverySink(ctx context.Context, updater discoveredRouteUpdater, logger *slog.Logger) *runtimeDiscoverySink {
	return &runtimeDiscoverySink{
		ctx:      ctx,
		updater:  updater,
		logger:   logger,
		readyCh:  make(chan struct{}),
		syncedCh: make(chan struct{}, 1),
	}
}

func (s *runtimeDiscoverySink) Update(routes []k8sdiscovery.DiscoveredRoute) {
	s.update(k8sdiscovery.NewSnapshotProvider(routes), len(routes), 0, 0)
}

func (s *runtimeDiscoverySink) UpdateResult(result k8sdiscovery.Result) {
	s.update(k8sdiscovery.NewSnapshotProviderFromResult(result), len(result.Routes), len(result.Skipped), len(result.DuplicateHosts))
}

func (s *runtimeDiscoverySink) ready() <-chan struct{} {
	return s.readyCh
}

func (s *runtimeDiscoverySink) synced() <-chan struct{} {
	return s.syncedCh
}

func (s *runtimeDiscoverySink) update(provider discovery.RouteProvider, routes, skipped, duplicateHosts int) {
	err := s.updater.UpdateDiscoveredRoutes(s.ctx, provider)
	s.readyOnce.Do(func() {
		close(s.readyCh)
	})
	select {
	case s.syncedCh <- struct{}{}:
	default:
	}
	if err != nil {
		logKubernetesRuntimeSnapshotFailed(s.logger, err, routes, skipped, duplicateHosts)
		return
	}
	logKubernetesRuntimeSnapshotUpdated(s.logger, routes, skipped, duplicateHosts)
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
