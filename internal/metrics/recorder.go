package metrics

import (
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	ConnectionResultAccepted = "accepted"
	ConnectionResultClosed   = "closed"
	ConnectionResultDenied   = "denied"
	ConnectionResultFailed   = "failed"

	ReasonBackendClose       = "backend_close"
	ReasonBackendDialFailed  = "backend_dial_failed"
	ReasonBackendDialTimeout = "backend_dial_timeout"
	ReasonClientClose        = "client_close"
	ReasonContextCancelled   = "context_cancelled"
	ReasonHandshakeMalformed = "handshake_malformed"
	ReasonHandshakeTimeout   = "handshake_timeout"
	ReasonInitialWriteFailed = "initial_write_failed"
	ReasonRouteDenied        = "route_denied"
	ReasonSuccess            = "success"
	ReasonUnknown            = "unknown"

	RouteDecisionDefault = "default"
	RouteDecisionDenied  = "denied"
	RouteDecisionMatched = "matched"

	FallbackStateLogin  = "login"
	FallbackStateStatus = "status"

	ReloadResultFailed  = "failed"
	ReloadResultSuccess = "success"

	KubernetesWatchRestartReasonListFailed       = "list_failed"
	KubernetesWatchRestartReasonWatchClosed      = "watch_closed"
	KubernetesWatchRestartReasonWatchError       = "watch_error"
	KubernetesWatchRestartReasonWatchSetupFailed = "watch_setup_failed"
	KubernetesWatchRestartReasonUnknown          = "unknown"

	KubernetesDiscoveryErrorReasonInClusterConfigFailed     = "incluster_config_failed"
	KubernetesDiscoveryErrorReasonInitialListFailed         = "initial_list_failed"
	KubernetesDiscoveryErrorReasonNamespaceResolutionFailed = "namespace_resolution_failed"
	KubernetesDiscoveryErrorReasonRebuildFailed             = "rebuild_failed"
	KubernetesDiscoveryErrorReasonWatchClosed               = "watch_closed"
	KubernetesDiscoveryErrorReasonWatchError                = "watch_error"
	KubernetesDiscoveryErrorReasonWatchSetupFailed          = "watch_setup_failed"
	KubernetesDiscoveryErrorReasonUnknown                   = "unknown"
)

type Recorder struct {
	enabled  bool
	registry *prometheus.Registry

	connectionsTotal                 *prometheus.CounterVec
	backendDialsTotal                *prometheus.CounterVec
	fallbackResponses                *prometheus.CounterVec
	reloadTotal                      *prometheus.CounterVec
	routeDecisionsTotal              *prometheus.CounterVec
	kubernetesWatchRestartsTotal     *prometheus.CounterVec
	kubernetesDiscoveryErrorsTotal   *prometheus.CounterVec
	activeConnections                prometheus.Gauge
	configGeneration                 prometheus.Gauge
	routes                           prometheus.Gauge
	kubernetesWatchRunning           prometheus.Gauge
	kubernetesLastSuccessfulSyncTime prometheus.Gauge
	kubernetesDiscoveredRoutes       prometheus.Gauge
	connectionDuration               prometheus.Histogram
	backendDialDuration              prometheus.Histogram
}

func NewRecorder(enabled bool) *Recorder {
	if !enabled {
		return &Recorder{}
	}
	r := &Recorder{
		enabled:  true,
		registry: prometheus.NewRegistry(),
		connectionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_connections_total",
			Help: "Minecraft gateway connection lifecycle events by low-cardinality result and reason.",
		}, []string{"result", "reason"}),
		backendDialsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_backend_dials_total",
			Help: "Minecraft gateway backend dial attempts by low-cardinality result and reason.",
		}, []string{"result", "reason"}),
		fallbackResponses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_fallback_responses_total",
			Help: "Minecraft gateway fallback responses successfully written by protocol state and low-cardinality reason.",
		}, []string{"state", "reason"}),
		reloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_reload_total",
			Help: "Minecraft gateway configuration reload attempts by result.",
		}, []string{"result"}),
		routeDecisionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_route_decisions_total",
			Help: "Minecraft gateway route decisions by result.",
		}, []string{"result"}),
		kubernetesWatchRestartsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_kubernetes_watch_restarts_total",
			Help: "Kubernetes discovery watch restarts by low-cardinality reason.",
		}, []string{"reason"}),
		kubernetesDiscoveryErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_kubernetes_discovery_errors_total",
			Help: "Kubernetes discovery errors by low-cardinality reason.",
		}, []string{"reason"}),
		activeConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_active_connections",
			Help: "Currently active accepted Minecraft gateway client connections.",
		}),
		configGeneration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_config_generation",
			Help: "Current in-memory configuration generation. Initial load is 1 and successful reloads increment it.",
		}),
		routes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_routes",
			Help: "Number of explicit routes in the current configuration. The default route is not included.",
		}),
		kubernetesWatchRunning: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_kubernetes_watch_running",
			Help: "Whether the Kubernetes discovery watch supervisor is running.",
		}),
		kubernetesLastSuccessfulSyncTime: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_kubernetes_last_successful_sync_timestamp_seconds",
			Help: "Unix timestamp of the last successful Kubernetes discovery sync applied to runtime routing.",
		}),
		kubernetesDiscoveredRoutes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_kubernetes_discovered_routes",
			Help: "Number of Kubernetes discovered routes in the latest successfully applied runtime snapshot.",
		}),
		connectionDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "mc_gateway_connection_duration_seconds",
			Help:    "Duration of accepted Minecraft gateway client connections.",
			Buckets: prometheus.DefBuckets,
		}),
		backendDialDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "mc_gateway_backend_dial_duration_seconds",
			Help:    "Duration of Minecraft gateway backend dial attempts.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	r.registry.MustRegister(
		r.connectionsTotal,
		r.backendDialsTotal,
		r.fallbackResponses,
		r.reloadTotal,
		r.routeDecisionsTotal,
		r.kubernetesWatchRestartsTotal,
		r.kubernetesDiscoveryErrorsTotal,
		r.activeConnections,
		r.configGeneration,
		r.routes,
		r.kubernetesWatchRunning,
		r.kubernetesLastSuccessfulSyncTime,
		r.kubernetesDiscoveredRoutes,
		r.connectionDuration,
		r.backendDialDuration,
	)
	return r
}

func (r *Recorder) Registry() *prometheus.Registry {
	return r.registry
}

func (r *Recorder) SetConfig(generation int64, cfg config.Config) {
	if !r.enabled {
		return
	}
	r.configGeneration.Set(float64(generation))
	r.routes.Set(float64(len(cfg.Routes)))
}

func (r *Recorder) ConnectionAccepted() {
	if !r.enabled {
		return
	}
	r.connectionsTotal.WithLabelValues(ConnectionResultAccepted, ReasonUnknown).Inc()
	r.activeConnections.Inc()
}

func (r *Recorder) ConnectionFinished(result string, reason string, duration time.Duration) {
	if !r.enabled {
		return
	}
	r.connectionsTotal.WithLabelValues(result, reason).Inc()
	r.activeConnections.Dec()
	r.connectionDuration.Observe(duration.Seconds())
}

func (r *Recorder) BackendDialFinished(result string, reason string, duration time.Duration) {
	if !r.enabled {
		return
	}
	r.backendDialsTotal.WithLabelValues(result, reason).Inc()
	r.backendDialDuration.Observe(duration.Seconds())
}

func (r *Recorder) FallbackResponse(state string, reason string) {
	if !r.enabled {
		return
	}
	r.fallbackResponses.WithLabelValues(state, reason).Inc()
}

func (r *Recorder) RouteDecision(result string) {
	if !r.enabled {
		return
	}
	r.routeDecisionsTotal.WithLabelValues(result).Inc()
}

func (r *Recorder) Reload(result string) {
	if !r.enabled {
		return
	}
	r.reloadTotal.WithLabelValues(result).Inc()
}

func (r *Recorder) KubernetesWatchRunning(running bool) {
	if !r.enabled {
		return
	}
	if running {
		r.kubernetesWatchRunning.Set(1)
		return
	}
	r.kubernetesWatchRunning.Set(0)
}

func (r *Recorder) KubernetesWatchRestart(reason string) {
	if !r.enabled {
		return
	}
	r.kubernetesWatchRestartsTotal.WithLabelValues(reason).Inc()
}

func (r *Recorder) KubernetesDiscoverySync(discoveredRoutes int) {
	if !r.enabled {
		return
	}
	r.kubernetesDiscoveredRoutes.Set(float64(discoveredRoutes))
	r.kubernetesLastSuccessfulSyncTime.Set(float64(time.Now().Unix()))
}

func (r *Recorder) KubernetesDiscoveryError(reason string) {
	if !r.enabled {
		return
	}
	r.kubernetesDiscoveryErrorsTotal.WithLabelValues(reason).Inc()
}
