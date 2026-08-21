package metrics

import (
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	ConnectionResultAccepted = "accepted"
	ConnectionResultClosed   = "closed"
	ConnectionResultDenied   = "denied"
	ConnectionResultFailed   = "failed"

	ReasonBackendClose         = "backend_close"
	ReasonBackendDialFailed    = "backend_dial_failed"
	ReasonBackendDialTimeout   = "backend_dial_timeout"
	ReasonBackendStatusFailed  = "backend_status_failed"
	ReasonBackendStatusInvalid = "backend_status_invalid"
	ReasonBackendStatusStale   = "backend_status_stale"
	ReasonBackendStatusTimeout = "backend_status_timeout"
	ReasonBackendStatusUnknown = "backend_status_unknown"
	ReasonClientClose          = "client_close"
	ReasonClientDenied         = "client_denied"
	ReasonContextCancelled     = "context_cancelled"
	ReasonHandshakeMalformed   = "handshake_malformed"
	ReasonHandshakeTimeout     = "handshake_timeout"
	ReasonInitialWriteFailed   = "initial_write_failed"
	ReasonRouteDenied          = "route_denied"
	ReasonRateLimited          = "rate_limited"
	ReasonSuccess              = "success"
	ReasonUnknown              = "unknown"

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

	UDPPacketDirectionClientToBackend = "client_to_backend"
	UDPPacketDirectionBackendToClient = "backend_to_client"

	UDPPacketResultForwarded           = "forwarded"
	UDPPacketResultDroppedSessionLimit = "dropped_session_limit"
	UDPPacketResultDroppedReadError    = "dropped_read_error"
	UDPPacketResultDroppedWriteError   = "dropped_write_error"

	UDPSessionCloseReasonIdleTimeout  = "idle_timeout"
	UDPSessionCloseReasonBackendError = "backend_error"
	UDPSessionCloseReasonShutdown     = "shutdown"

	VoiceChatPacketDirectionClientToBackend = "client_to_backend"
	VoiceChatPacketDirectionBackendToClient = "backend_to_client"

	VoiceChatPacketResultForwarded                  = "forwarded"
	VoiceChatPacketResultDroppedUnknownSession      = "dropped_unknown_session"
	VoiceChatPacketResultDroppedExpiredRegistration = "dropped_expired_registration"
	VoiceChatPacketResultDroppedMalformed           = "dropped_malformed"
	VoiceChatPacketResultDroppedSessionLimit        = "dropped_session_limit"
	VoiceChatPacketResultDroppedReadError           = "dropped_read_error"
	VoiceChatPacketResultDroppedWriteError          = "dropped_write_error"

	VoiceChatSessionCloseReasonIdleTimeout         = "idle_timeout"
	VoiceChatSessionCloseReasonBackendError        = "backend_error"
	VoiceChatSessionCloseReasonShutdown            = "shutdown"
	VoiceChatSessionCloseReasonReassigned          = "reassigned"
	VoiceChatSessionCloseReasonRegistrationExpired = "registration_expired"
	VoiceChatSessionCloseReasonUnregistered        = "unregistered"

	VoiceChatRegistrationResultCreated    = "created"
	VoiceChatRegistrationResultReplaced   = "replaced"
	VoiceChatRegistrationResultRefreshed  = "refreshed"
	VoiceChatRegistrationResultDeleted    = "deleted"
	VoiceChatRegistrationResultExpired    = "expired"
	VoiceChatRegistrationResultAuthFailed = "auth_failed"
	VoiceChatRegistrationResultMalformed  = "malformed"
	VoiceChatRegistrationResultLimit      = "limit"
	VoiceChatRegistrationResultStaleLease = "stale_lease"
	VoiceChatRegistrationResultFailed     = "failed"
)

var kubernetesSkippedServiceReasons = []string{
	k8sdiscovery.ReasonDisabled,
	k8sdiscovery.ReasonInvalidPrefix,
	k8sdiscovery.ReasonInvalidServiceName,
	k8sdiscovery.ReasonInvalidNamespace,
	k8sdiscovery.ReasonMissingHost,
	k8sdiscovery.ReasonInvalidHost,
	k8sdiscovery.ReasonMissingPort,
	k8sdiscovery.ReasonInvalidPort,
	k8sdiscovery.ReasonPortNotFound,
	k8sdiscovery.ReasonDuplicateHost,
	k8sdiscovery.ReasonUnknown,
}

type Recorder struct {
	enabled  bool
	registry *prometheus.Registry

	connectionsTotal                 *prometheus.CounterVec
	backendDialsTotal                *prometheus.CounterVec
	statusSourceProbes               *prometheus.CounterVec
	fallbackResponses                *prometheus.CounterVec
	reloadTotal                      *prometheus.CounterVec
	routeDecisionsTotal              *prometheus.CounterVec
	kubernetesWatchRestartsTotal     *prometheus.CounterVec
	kubernetesDiscoveryErrorsTotal   *prometheus.CounterVec
	kubernetesSkippedServices        *prometheus.GaugeVec
	udpPacketsTotal                  *prometheus.CounterVec
	udpBytesTotal                    *prometheus.CounterVec
	udpSessions                      prometheus.Gauge
	udpSessionsCreatedTotal          prometheus.Counter
	udpSessionsClosedTotal           *prometheus.CounterVec
	voicechatPacketsTotal            *prometheus.CounterVec
	voicechatBytesTotal              *prometheus.CounterVec
	voicechatSessions                prometheus.Gauge
	voicechatSessionsCreatedTotal    prometheus.Counter
	voicechatSessionsClosedTotal     *prometheus.CounterVec
	voicechatRegistrations           prometheus.Gauge
	voicechatRegistrationEventsTotal *prometheus.CounterVec
	voicechatBackendSwitchesTotal    prometheus.Counter
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
		statusSourceProbes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_status_source_probes_total",
			Help: "Router-owned Java status source probe completions by low-cardinality result and reason.",
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
		kubernetesSkippedServices: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "mc_gateway_kubernetes_skipped_services",
			Help: "Number of Kubernetes Services skipped in the latest successfully applied discovery snapshot by low-cardinality reason.",
		}, []string{"reason"}),
		udpPacketsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_udp_packets_total",
			Help: "UDP relay packet events by low-cardinality direction and result.",
		}, []string{"direction", "result"}),
		udpBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_udp_bytes_total",
			Help: "UDP relay bytes forwarded by low-cardinality direction.",
		}, []string{"direction"}),
		udpSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_udp_sessions",
			Help: "Currently active UDP relay transport sessions.",
		}),
		udpSessionsCreatedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mc_gateway_udp_sessions_created_total",
			Help: "UDP relay transport sessions created.",
		}),
		udpSessionsClosedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_udp_sessions_closed_total",
			Help: "UDP relay transport sessions closed by low-cardinality reason.",
		}, []string{"reason"}),
		voicechatPacketsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_voicechat_packets_total",
			Help: "Dynamic Simple Voice Chat packet events by low-cardinality direction and result.",
		}, []string{"direction", "result"}),
		voicechatBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_voicechat_bytes_total",
			Help: "Dynamic Simple Voice Chat bytes forwarded by low-cardinality direction.",
		}, []string{"direction"}),
		voicechatSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_voicechat_sessions",
			Help: "Currently active dynamic Simple Voice Chat UDP transport sessions.",
		}),
		voicechatSessionsCreatedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mc_gateway_voicechat_sessions_created_total",
			Help: "Dynamic Simple Voice Chat UDP transport sessions created.",
		}),
		voicechatSessionsClosedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_voicechat_sessions_closed_total",
			Help: "Dynamic Simple Voice Chat UDP transport sessions closed by low-cardinality reason.",
		}, []string{"reason"}),
		voicechatRegistrations: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mc_gateway_voicechat_registrations",
			Help: "Currently active dynamic Simple Voice Chat backend registrations.",
		}),
		voicechatRegistrationEventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mc_gateway_voicechat_registration_events_total",
			Help: "Dynamic Simple Voice Chat registration events by low-cardinality result.",
		}, []string{"result"}),
		voicechatBackendSwitchesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mc_gateway_voicechat_backend_switches_total",
			Help: "Dynamic Simple Voice Chat backend ownership replacements where the backend changed.",
		}),
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
		r.statusSourceProbes,
		r.fallbackResponses,
		r.reloadTotal,
		r.routeDecisionsTotal,
		r.kubernetesWatchRestartsTotal,
		r.kubernetesDiscoveryErrorsTotal,
		r.kubernetesSkippedServices,
		r.udpPacketsTotal,
		r.udpBytesTotal,
		r.udpSessions,
		r.udpSessionsCreatedTotal,
		r.udpSessionsClosedTotal,
		r.voicechatPacketsTotal,
		r.voicechatBytesTotal,
		r.voicechatSessions,
		r.voicechatSessionsCreatedTotal,
		r.voicechatSessionsClosedTotal,
		r.voicechatRegistrations,
		r.voicechatRegistrationEventsTotal,
		r.voicechatBackendSwitchesTotal,
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

func (r *Recorder) StatusSourceProbe(result string, reason string) {
	if !r.enabled {
		return
	}
	r.statusSourceProbes.WithLabelValues(result, reason).Inc()
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

func (r *Recorder) KubernetesSkippedServicesByReason(skippedByReason map[string]int) {
	if !r.enabled {
		return
	}
	for _, reason := range kubernetesSkippedServiceReasons {
		r.kubernetesSkippedServices.WithLabelValues(reason).Set(float64(skippedByReason[reason]))
	}
}

func (r *Recorder) KubernetesDiscoveryError(reason string) {
	if !r.enabled {
		return
	}
	r.kubernetesDiscoveryErrorsTotal.WithLabelValues(reason).Inc()
}

func (r *Recorder) UDPPacket(direction string, result string, bytes int) {
	if !r.enabled {
		return
	}
	r.udpPacketsTotal.WithLabelValues(direction, result).Inc()
	if result == UDPPacketResultForwarded {
		r.udpBytesTotal.WithLabelValues(direction).Add(float64(bytes))
	}
}

func (r *Recorder) UDPSessionCreated() {
	if !r.enabled {
		return
	}
	r.udpSessionsCreatedTotal.Inc()
	r.udpSessions.Inc()
}

func (r *Recorder) UDPSessionClosed(reason string) {
	if !r.enabled {
		return
	}
	r.udpSessionsClosedTotal.WithLabelValues(reason).Inc()
	r.udpSessions.Dec()
}

func (r *Recorder) VoiceChatPacket(direction string, result string, bytes int) {
	if !r.enabled {
		return
	}
	r.voicechatPacketsTotal.WithLabelValues(direction, result).Inc()
	if result == VoiceChatPacketResultForwarded {
		r.voicechatBytesTotal.WithLabelValues(direction).Add(float64(bytes))
	}
}

func (r *Recorder) VoiceChatSessionCreated() {
	if !r.enabled {
		return
	}
	r.voicechatSessionsCreatedTotal.Inc()
	r.voicechatSessions.Inc()
}

func (r *Recorder) VoiceChatSessionClosed(reason string) {
	if !r.enabled {
		return
	}
	r.voicechatSessionsClosedTotal.WithLabelValues(reason).Inc()
	r.voicechatSessions.Dec()
}

func (r *Recorder) VoiceChatRegistrationsSet(count int) {
	if !r.enabled {
		return
	}
	r.voicechatRegistrations.Set(float64(count))
}

func (r *Recorder) VoiceChatRegistrationEvent(result string) {
	if !r.enabled {
		return
	}
	r.voicechatRegistrationEventsTotal.WithLabelValues(result).Inc()
}

func (r *Recorder) VoiceChatBackendSwitch() {
	if !r.enabled {
		return
	}
	r.voicechatBackendSwitchesTotal.Inc()
}
