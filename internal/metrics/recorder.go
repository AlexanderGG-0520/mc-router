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

	FallbackStateStatus = "status"

	ReloadResultFailed  = "failed"
	ReloadResultSuccess = "success"
)

type Recorder struct {
	enabled  bool
	registry *prometheus.Registry

	connectionsTotal    *prometheus.CounterVec
	backendDialsTotal   *prometheus.CounterVec
	fallbackResponses   *prometheus.CounterVec
	reloadTotal         *prometheus.CounterVec
	routeDecisionsTotal *prometheus.CounterVec
	activeConnections   prometheus.Gauge
	configGeneration    prometheus.Gauge
	routes              prometheus.Gauge
	connectionDuration  prometheus.Histogram
	backendDialDuration prometheus.Histogram
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
		r.activeConnections,
		r.configGeneration,
		r.routes,
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
