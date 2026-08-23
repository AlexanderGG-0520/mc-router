package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/clientpolicy"
	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
	"github.com/AlexanderGG-0520/mc-router/internal/proxyprotocol"
	"github.com/AlexanderGG-0520/mc-router/internal/ratelimit"
	"github.com/AlexanderGG-0520/mc-router/internal/router"
	"github.com/AlexanderGG-0520/mc-router/internal/scaler"
)

type Server struct {
	state   atomic.Pointer[serverState]
	logger  *slog.Logger
	limits  mcproto.Limits
	metrics *gatewaymetrics.Recorder

	listenAddress string
	dialContext   dialContextFunc
	connectionSeq atomic.Uint64

	listenerMu sync.Mutex
	listener   net.Listener
	wg         sync.WaitGroup

	snapshotMu sync.Mutex
}

type serverState struct {
	generation       int64
	staticConfig     config.Config
	cfg              config.Config
	router           *router.Router
	clientPolicy     *clientpolicy.Policy
	clientRateLimit  *ratelimit.Limiter
	trustedProxies   []netip.Prefix
	scalerWebhook    *scaler.Client
	discoveryMerge   discovery.MergeResult
	discoveredRoutes []kubernetes.DiscoveredRoute
}

type dialContextFunc func(ctx context.Context, network string, address string) (net.Conn, error)

type RouteSnapshot struct {
	StaticConfig     config.Config
	Config           config.Config
	Router           *router.Router
	DiscoveryMerge   discovery.MergeResult
	DiscoveredRoutes []kubernetes.DiscoveredRoute
}

const (
	reasonBackendClose       = gatewaymetrics.ReasonBackendClose
	reasonBackendDialFailed  = gatewaymetrics.ReasonBackendDialFailed
	reasonBackendDialTimeout = gatewaymetrics.ReasonBackendDialTimeout
	reasonClientClose        = gatewaymetrics.ReasonClientClose
	reasonContextCancelled   = gatewaymetrics.ReasonContextCancelled
	reasonHandshakeMalformed = gatewaymetrics.ReasonHandshakeMalformed
	reasonHandshakeTimeout   = gatewaymetrics.ReasonHandshakeTimeout
	reasonInitialWriteFailed = gatewaymetrics.ReasonInitialWriteFailed
	reasonRouteDenied        = gatewaymetrics.ReasonRouteDenied
)

func NewServer(cfg config.Config, routeTable *router.Router, logger *slog.Logger) *Server {
	return newServer(RouteSnapshot{Config: cfg, Router: routeTable}, logger)
}

func NewServerFromSnapshot(snapshot RouteSnapshot, logger *slog.Logger) *Server {
	return newServer(snapshot, logger)
}

func BuildRouteSnapshot(cfg config.Config, discoveredRoutes []kubernetes.DiscoveredRoute) (RouteSnapshot, error) {
	if err := cfg.Validate(); err != nil {
		return RouteSnapshot{}, err
	}
	merge := discovery.MergeRoutes(cfg.Routes, discoveredRoutes, discovery.MergeOptions{
		DefaultRoute: cfg.DefaultRoute,
	})
	mergedConfig := cloneConfig(cfg)
	mergedConfig.Routes = merge.Routes
	routeTable, err := router.New(mergedConfig)
	if err != nil {
		return RouteSnapshot{}, err
	}
	return RouteSnapshot{
		StaticConfig:     cloneConfig(cfg),
		Config:           mergedConfig,
		Router:           routeTable,
		DiscoveryMerge:   merge,
		DiscoveredRoutes: append([]kubernetes.DiscoveredRoute(nil), discoveredRoutes...),
	}, nil
}

// RebuildRouteSnapshot creates a new RouteSnapshot by combining the validated configuration
// and routes from the given provider. If provider is nil, it behaves like BuildRouteSnapshot(cfg, nil).
func RebuildRouteSnapshot(ctx context.Context, cfg config.Config, provider discovery.RouteProvider) (RouteSnapshot, error) {
	if err := cfg.Validate(); err != nil {
		return RouteSnapshot{}, err
	}
	var discovered []kubernetes.DiscoveredRoute
	if provider != nil {
		var err error
		discovered, err = provider.Routes(ctx)
		if err != nil {
			return RouteSnapshot{}, err
		}
		discovered = append([]kubernetes.DiscoveredRoute(nil), discovered...)
	}
	return BuildRouteSnapshot(cfg, discovered)
}

func newServer(snapshot RouteSnapshot, logger *slog.Logger) *Server {
	snapshot = cloneRouteSnapshot(snapshot)
	staticConfig := snapshot.StaticConfig
	if staticConfig.Listen == "" {
		staticConfig = snapshot.Config
	}
	cfg := snapshot.Config
	routeTable := snapshot.Router
	if routeTable == nil {
		panic("proxy: nil router")
	}
	dialer := net.Dialer{}
	recorder := gatewaymetrics.NewRecorder(cfg.Metrics.Enabled)
	s := &Server{
		logger:        logger,
		limits:        mcproto.DefaultLimits(),
		metrics:       recorder,
		listenAddress: cfg.Listen,
		dialContext:   dialer.DialContext,
	}
	s.state.Store(newServerState(1, staticConfig, cfg, routeTable, snapshot.DiscoveryMerge, snapshot.DiscoveredRoutes))
	recorder.SetConfig(1, cfg)
	if staticConfig.Discovery.Kubernetes.Enabled {
		recorder.KubernetesDiscoverySync(len(snapshot.DiscoveredRoutes))
	}
	return s
}

func (s *Server) Metrics() *gatewaymetrics.Recorder {
	return s.metrics
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", s.listenAddress)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

func (s *Server) ReloadFile(path string) error {
	cfg, err := config.LoadFile(path)
	if err != nil {
		s.metrics.Reload(gatewaymetrics.ReloadResultFailed)
		s.logger.Error("reload_failed", "config", path, "error", err)
		return err
	}

	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()

	current := s.currentState()
	return s.reloadConfigLocked(path, cfg, current.discoveredRoutes)
}

func (s *Server) ReloadConfigWithDiscovery(ctx context.Context, path string, cfg config.Config, provider discovery.RouteProvider) error {
	discoveredRoutes, err := discoveredRoutesFromProvider(ctx, provider)
	if err != nil {
		s.metrics.Reload(gatewaymetrics.ReloadResultFailed)
		s.logger.Error("reload_failed", "config", path, "error", err)
		return err
	}

	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()

	return s.reloadConfigLocked(path, cfg, discoveredRoutes)
}

func (s *Server) reloadConfigLocked(path string, cfg config.Config, discoveredRoutes []kubernetes.DiscoveredRoute) error {
	snapshot, err := BuildRouteSnapshot(cfg, discoveredRoutes)
	if err != nil {
		s.metrics.Reload(gatewaymetrics.ReloadResultFailed)
		s.logger.Error("reload_failed", "config", path, "error", err)
		return err
	}
	s.updateRouteSnapshotLocked(snapshot)
	s.metrics.Reload(gatewaymetrics.ReloadResultSuccess)
	s.logger.Info(
		"reload_success",
		"config", path,
		"routes", len(snapshot.Config.Routes),
		"listen", s.listenAddress,
		"configured_listen", snapshot.Config.Listen,
		"listen_change_ignored", snapshot.Config.Listen != s.listenAddress,
	)
	return nil
}

func (s *Server) UpdateConfig(cfg config.Config, routeTable *router.Router) {
	s.UpdateRouteSnapshot(RouteSnapshot{Config: cfg, Router: routeTable})
}

func (s *Server) UpdateRouteSnapshot(snapshot RouteSnapshot) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.updateRouteSnapshotLocked(snapshot)
}

func (s *Server) UpdateDiscoveredRoutes(ctx context.Context, provider discovery.RouteProvider) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	discoveredRoutes, err := discoveredRoutesFromProvider(ctx, provider)
	if err != nil {
		s.metrics.KubernetesDiscoveryError(gatewaymetrics.KubernetesDiscoveryErrorReasonUnknown)
		return err
	}

	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	current := s.currentState()
	snapshot, err := BuildRouteSnapshot(current.staticConfig, discoveredRoutes)
	if err != nil {
		s.metrics.KubernetesDiscoveryError(gatewaymetrics.KubernetesDiscoveryErrorReasonRebuildFailed)
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.updateRouteSnapshotLocked(snapshot)
	s.metrics.KubernetesDiscoverySync(len(discoveredRoutes))
	return nil
}

func discoveredRoutesFromProvider(ctx context.Context, provider discovery.RouteProvider) ([]kubernetes.DiscoveredRoute, error) {
	if provider == nil {
		return nil, nil
	}
	discoveredRoutes, err := provider.Routes(ctx)
	if err != nil {
		return nil, err
	}
	return append([]kubernetes.DiscoveredRoute(nil), discoveredRoutes...), nil
}

func (s *Server) Snapshot() RouteSnapshot {
	return cloneRouteSnapshot(stateToSnapshot(s.currentState()))
}

func (s *Server) updateRouteSnapshotLocked(snapshot RouteSnapshot) {
	snapshot = cloneRouteSnapshot(snapshot)
	staticConfig := snapshot.StaticConfig
	if staticConfig.Listen == "" {
		staticConfig = snapshot.Config
	}
	cfg := snapshot.Config
	routeTable := snapshot.Router
	if routeTable == nil {
		panic("proxy: nil router")
	}
	generation := s.currentState().generation + 1
	s.state.Store(newServerState(generation, staticConfig, cfg, routeTable, snapshot.DiscoveryMerge, snapshot.DiscoveredRoutes))
	s.metrics.SetConfig(generation, cfg)
}

func cloneRouteSnapshot(snapshot RouteSnapshot) RouteSnapshot {
	snapshot.StaticConfig = cloneConfig(snapshot.StaticConfig)
	snapshot.Config = cloneConfig(snapshot.Config)
	snapshot.DiscoveredRoutes = append([]kubernetes.DiscoveredRoute(nil), snapshot.DiscoveredRoutes...)
	snapshot.DiscoveryMerge.Routes = append([]config.Route(nil), snapshot.DiscoveryMerge.Routes...)
	snapshot.DiscoveryMerge.Ignored = append([]discovery.IgnoredDiscoveredRoute(nil), snapshot.DiscoveryMerge.Ignored...)
	snapshot.DiscoveryMerge.Stats.IgnoredByReason = cloneStringIntMap(snapshot.DiscoveryMerge.Stats.IgnoredByReason)
	return snapshot
}

func cloneConfig(cfg config.Config) config.Config {
	cfg.ClientPolicy.Allow = append([]string(nil), cfg.ClientPolicy.Allow...)
	cfg.ClientPolicy.Deny = append([]string(nil), cfg.ClientPolicy.Deny...)
	cfg.Routes = append([]config.Route(nil), cfg.Routes...)
	for i := range cfg.Routes {
		cfg.Routes[i].Aliases = append([]string(nil), cfg.Routes[i].Aliases...)
		if cfg.Routes[i].StatusOverride != nil {
			override := *cfg.Routes[i].StatusOverride
			cfg.Routes[i].StatusOverride = &override
		}
	}
	if cfg.Fallback.Login.RespondOnRouteDenied != nil {
		value := *cfg.Fallback.Login.RespondOnRouteDenied
		cfg.Fallback.Login.RespondOnRouteDenied = &value
	}
	if cfg.Fallback.Status.RespondOnRouteDenied != nil {
		value := *cfg.Fallback.Status.RespondOnRouteDenied
		cfg.Fallback.Status.RespondOnRouteDenied = &value
	}
	return cfg
}

func cloneStringIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func newServerState(generation int64, staticConfig, cfg config.Config, routeTable *router.Router, merge discovery.MergeResult, discoveredRoutes []kubernetes.DiscoveredRoute) *serverState {
	policy, err := clientpolicy.New(cfg.ClientPolicy.Allow, cfg.ClientPolicy.Deny)
	if err != nil {
		panic("proxy: invalid validated client policy: " + err.Error())
	}
	trustedProxies, err := proxyprotocol.ParseCIDRs(cfg.ProxyProtocol.TrustedProxies)
	if err != nil {
		panic("proxy: invalid validated trusted proxies: " + err.Error())
	}
	return &serverState{
		generation:     generation,
		staticConfig:   cloneConfig(staticConfig),
		cfg:            cfg,
		router:         routeTable,
		clientPolicy:   policy,
		trustedProxies: trustedProxies,
		scalerWebhook:  scaler.New(scaler.Config{Enabled: cfg.ScalerWebhook.Enabled, URL: cfg.ScalerWebhook.URL, Timeout: cfg.ScalerWebhook.Timeout.Duration, Headers: cfg.ScalerWebhook.Headers}),
		clientRateLimit: ratelimit.New(ratelimit.Config{
			Enabled:              cfg.ClientRateLimit.Enabled,
			ConnectionsPerSecond: cfg.ClientRateLimit.ConnectionsPerSecond,
			Burst:                cfg.ClientRateLimit.Burst,
			IdleTimeout:          cfg.ClientRateLimit.IdleTimeout.Duration,
			MaxEntries:           cfg.ClientRateLimit.MaxEntries,
		}),
		discoveryMerge:   merge,
		discoveredRoutes: discoveredRoutes,
	}
}

func (s *Server) currentState() *serverState {
	state := s.state.Load()
	if state == nil {
		panic("proxy: server state is not initialized")
	}
	return state
}

func stateToSnapshot(state *serverState) RouteSnapshot {
	return RouteSnapshot{
		StaticConfig:     state.staticConfig,
		Config:           state.cfg,
		Router:           state.router,
		DiscoveryMerge:   state.discoveryMerge,
		DiscoveredRoutes: state.discoveredRoutes,
	}
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.listenerMu.Lock()
	s.listener = listener
	s.listenerMu.Unlock()

	s.logger.Info("listening", "address", listener.Addr().String())
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				s.wg.Wait()
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Temporary() {
				s.logger.Warn("temporary accept error", "error", err)
				continue
			}
			s.wg.Wait()
			return err
		}
		s.wg.Add(1)
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) Shutdown() {
	s.listenerMu.Lock()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.listenerMu.Unlock()
	s.wg.Wait()
}

func (s *Server) handleConn(ctx context.Context, client net.Conn) {
	defer s.wg.Done()
	defer client.Close()
	start := time.Now()
	connectionResult := gatewaymetrics.ConnectionResultFailed
	connectionReason := gatewaymetrics.ReasonUnknown
	s.metrics.ConnectionAccepted()
	defer func() {
		s.metrics.ConnectionFinished(connectionResult, connectionReason, time.Since(start))
	}()

	state := s.currentState()
	connectionID := fmt.Sprintf("c-%016x", s.connectionSeq.Add(1))
	configGeneration := state.generation
	remoteAddr := client.RemoteAddr().String()
	logAttrs := func(stage string) []any {
		return []any{"connection_id", connectionID, "config_generation", configGeneration, "stage", stage, "remote", remoteAddr}
	}
	clientAddr, err := addressFromAddr(client.RemoteAddr())
	if err == nil && proxyprotocol.Trusted(state.trustedProxies, clientAddr) {
		clientAddr, err = proxyprotocol.Read(client)
		if err != nil {
			connectionResult = gatewaymetrics.ConnectionResultDenied
			connectionReason = reasonHandshakeMalformed
			s.logger.Warn("connection rejected", append(logAttrs("proxy_protocol"), "reason", "proxy_protocol_invalid", "error_kind", "proxy_protocol_invalid")...)
			return
		}
		remoteAddr = clientAddr.String()
	}
	if err != nil || !state.clientPolicy.Allows(clientAddr) {
		connectionResult = gatewaymetrics.ConnectionResultDenied
		connectionReason = gatewaymetrics.ReasonClientDenied
		if err != nil {
			s.logger.Warn("connection rejected", append(logAttrs("client_identity"), "reason", connectionReason, "error_kind", "client_address_invalid", "error", err)...)
			return
		}
		s.logger.Info("connection rejected", append(logAttrs("client_policy"), "reason", connectionReason, "error_kind", "client_policy_denied")...)
		return
	}
	if !state.clientRateLimit.Allow(clientAddr) {
		connectionResult = gatewaymetrics.ConnectionResultDenied
		connectionReason = gatewaymetrics.ReasonRateLimited
		s.logger.Info("connection rejected", append(logAttrs("client_rate_limit"), "reason", connectionReason, "error_kind", "rate_limited")...)
		return
	}
	if err := client.SetReadDeadline(time.Now().Add(state.cfg.HandshakeTimeout.Duration)); err != nil {
		s.logger.Warn("failed to set handshake deadline", "remote", remoteAddr, "error", err)
		return
	}
	handshake, rawHandshake, err := mcproto.ReadHandshake(client, s.limits)
	if err != nil {
		reason := reasonHandshakeMalformed
		if isTimeout(err) {
			reason = reasonHandshakeTimeout
		}
		connectionResult = gatewaymetrics.ConnectionResultDenied
		connectionReason = reason
		s.logger.Warn("connection rejected", append(logAttrs("handshake"), "reason", reason, "error_kind", handshakeErrorKind(err), "error", err)...)
		return
	}
	if err := client.SetReadDeadline(time.Time{}); err != nil {
		s.logger.Warn("failed to clear handshake deadline", "remote", remoteAddr, "error", err)
		return
	}

	routeAddress := handshake.RouteAddress()
	selection, err := state.router.Select(routeAddress)
	if err != nil {
		s.metrics.RouteDecision(gatewaymetrics.RouteDecisionDenied)
		connectionResult = gatewaymetrics.ConnectionResultDenied
		connectionReason = reasonRouteDenied
		rejectionAttrs := append(logAttrs("route_selection"),
			"reason", reasonRouteDenied,
			"error_kind", routeSelectionErrorKind(err),
			"route_address", routeAddress,
			"intent", intentName(handshake.NextState),
			"server_port", handshake.ServerPort,
			"route_match", "none",
			"unknown_host_policy", state.cfg.UnknownHostPolicy,
			"default_route_configured", state.cfg.DefaultRoute.Backend != "",
			"error", err,
		)
		s.logger.Info("connection rejected", rejectionAttrs...)
		if statusFallbackForRouteDeniedEnabled(state.cfg, handshake) {
			if err := s.serveStatusFallback(client, state.cfg, remoteAddr, routeAddress, reasonRouteDenied, "", logAttrs("fallback_status")); err != nil {
				s.logger.Warn("fallback status response failed", append(logAttrs("fallback_status"), "reason", reasonRouteDenied, "state", "status", "server_address", routeAddress, "error", err)...)
			}
			return
		}
		if loginFallbackForRouteDeniedEnabled(state.cfg, handshake) {
			if err := s.serveLoginDisconnectFallback(client, state.cfg, handshake, remoteAddr, routeAddress, logAttrs("fallback_login")); err != nil {
				s.logger.Warn("fallback login disconnect failed", append(logAttrs("fallback_login"), "reason", reasonRouteDenied, "state", "login", "server_address", routeAddress, "error", err)...)
			}
			return
		}
		return
	}
	s.metrics.RouteDecision(routeDecisionResult(selection.MatchedBy))
	routeAttrs := selectionLogAttrs(routeAddress, handshake, selection)
	if handshake.NextState == mcproto.NextStateStatus && selection.StatusOverride != nil {
		if err := s.serveStatusOverride(client, state.cfg.HandshakeTimeout.Duration, *selection.StatusOverride); err != nil {
			s.logger.Warn("route status override response failed", append(logAttrs("status_override"), append(routeAttrs, "reason", reasonInitialWriteFailed, "error_kind", "status_override_failed", "error", err)...)...)
			return
		}
		connectionResult = gatewaymetrics.ConnectionResultClosed
		connectionReason = gatewaymetrics.ReasonSuccess
		s.logger.Info("route status override response sent", append(logAttrs("status_override"), append(routeAttrs, "reason", gatewaymetrics.ReasonSuccess)...)...)
		return
	}
	backendAddress, backendRole := selectedBackend(handshake, selection)
	routeAttrs = append(routeAttrs, "backend_role", backendRole, "selected_backend", backendAddress)

	if err := state.scalerWebhook.Notify(ctx, scaler.Event{Backend: backendAddress, ServerAddress: routeAddress, NextState: handshake.NextState}); err != nil {
		attrs := append(logAttrs("scaler_webhook"), routeAttrs...)
		attrs = append(attrs, "backend", backendAddress, "error", err)
		s.logger.Warn("scaler webhook failed", attrs...)
	}

	dialCtx, cancel := context.WithTimeout(ctx, state.cfg.BackendDialTimeout.Duration)
	defer cancel()
	dialStart := time.Now()
	backend, err := s.dialContext(dialCtx, "tcp", backendAddress)
	if err != nil {
		reason := classifyDialError(err)
		s.metrics.BackendDialFinished(gatewaymetrics.ConnectionResultFailed, reason, time.Since(dialStart))
		connectionResult = gatewaymetrics.ConnectionResultFailed
		connectionReason = reason
		if statusFallbackForBackendFailureEnabled(state.cfg, handshake, reason) {
			fallbackAttrs := append(logAttrs("fallback_status"), routeAttrs...)
			if err := s.serveStatusFallback(client, state.cfg, remoteAddr, routeAddress, reason, backendAddress, fallbackAttrs); err != nil {
				s.logger.Warn("fallback status response failed", append(fallbackAttrs, "reason", reason, "state", "status", "server_address", routeAddress, "backend", backendAddress, "error", err)...)
			}
			return
		}
		s.logger.Warn("connection rejected", append(logAttrs("backend_dial"), append(routeAttrs, "reason", reason, "error_kind", reason, "error", err)...)...)
		return
	}
	s.metrics.BackendDialFinished(gatewaymetrics.ReasonSuccess, gatewaymetrics.ReasonSuccess, time.Since(dialStart))
	defer backend.Close()

	if err := writeAll(backend, rawHandshake); err != nil {
		connectionResult = gatewaymetrics.ConnectionResultFailed
		connectionReason = reasonInitialWriteFailed
		s.logger.Warn("connection rejected", append(logAttrs("backend_initial_write"), append(routeAttrs, "reason", reasonInitialWriteFailed, "error_kind", "initial_write_failed", "error", err)...)...)
		return
	}

	proxyStartedAttrs := append(logAttrs("proxy_started"),
		"server_address", routeAddress,
		"next_state", handshake.NextState,
		"backend", backendAddress,
		"matched_by", selection.MatchedBy,
	)
	proxyStartedAttrs = append(proxyStartedAttrs, routeAttrs...)
	s.logger.Info("proxying connection", proxyStartedAttrs...)
	result := s.proxy(ctx, client, backend)
	connectionResult = gatewaymetrics.ConnectionResultClosed
	connectionReason = result.reason
	proxyClosedAttrs := append(logAttrs("proxy_closed"),
		"reason", result.reason,
		"server_address", routeAddress,
		"backend", backendAddress,
		"direction", result.direction,
		"bytes_copied", result.bytesCopied,
	)
	proxyClosedAttrs = append(proxyClosedAttrs, routeAttrs...)
	s.logger.Info("proxy connection closed", proxyClosedAttrs...)
}

func addressFromAddr(addr net.Addr) (netip.Addr, error) {
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		parsed, ok := netip.AddrFromSlice(tcpAddr.IP)
		if !ok {
			return netip.Addr{}, fmt.Errorf("parse client TCP address %q", addr)
		}
		return parsed.Unmap(), nil
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return netip.Addr{}, fmt.Errorf("split client address %q: %w", addr, err)
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse client address %q: %w", host, err)
	}
	return parsed.Unmap(), nil
}

func routeDecisionResult(matchedBy string) string {
	if matchedBy == "default" {
		return gatewaymetrics.RouteDecisionDefault
	}
	return gatewaymetrics.RouteDecisionMatched
}

func intentName(nextState int32) string {
	switch nextState {
	case mcproto.NextStateStatus:
		return "status"
	case mcproto.NextStateLogin:
		return "login"
	case mcproto.NextStateTransfer:
		return "transfer"
	default:
		return "unknown"
	}
}

func handshakeErrorKind(err error) string {
	switch {
	case errors.Is(err, hostaddr.ErrInvalid):
		return "handshake_invalid_server_address"
	case errors.Is(err, mcproto.ErrPacketTooLarge):
		return "handshake_packet_too_large"
	case errors.Is(err, mcproto.ErrUnsupportedNextState):
		return "handshake_unsupported_next_state"
	default:
		return "handshake_malformed"
	}
}

func routeSelectionErrorKind(err error) string {
	switch {
	case errors.Is(err, router.ErrInvalidServerAddress):
		return "route_address_invalid"
	case errors.Is(err, router.ErrNoRoute):
		return "route_not_found"
	default:
		return "route_selection_failed"
	}
}

func selectionLogAttrs(routeAddress string, handshake mcproto.Handshake, selection router.Selection) []any {
	attrs := []any{
		"route_address", routeAddress,
		"intent", intentName(handshake.NextState),
		"server_port", handshake.ServerPort,
		"route_match", selection.MatchKind,
		"route_source", selection.RouteSource,
	}
	if selection.CanonicalServerAddress != "" {
		attrs = append(attrs, "canonical_server_address", selection.CanonicalServerAddress)
	}
	if selection.StatusOverride != nil && handshake.NextState == mcproto.NextStateStatus {
		return append(attrs, "backend_role", "status_override")
	}
	return attrs
}

func selectedBackend(handshake mcproto.Handshake, selection router.Selection) (string, string) {
	if handshake.NextState == mcproto.NextStateStatus && selection.StatusBackend != "" {
		return selection.StatusBackend, "status_backend"
	}
	return selection.Backend, "backend"
}

func statusFallbackForRouteDeniedEnabled(cfg config.Config, handshake mcproto.Handshake) bool {
	if !statusFallbackEnabled(cfg, handshake) {
		return false
	}
	return cfg.Fallback.Status.RespondOnRouteDenied == nil || *cfg.Fallback.Status.RespondOnRouteDenied
}

func statusFallbackForBackendFailureEnabled(cfg config.Config, handshake mcproto.Handshake, reason string) bool {
	if !statusFallbackEnabled(cfg, handshake) || !cfg.Fallback.Status.RespondOnBackendFailure {
		return false
	}
	return reason == reasonBackendDialFailed || reason == reasonBackendDialTimeout
}

func statusFallbackEnabled(cfg config.Config, handshake mcproto.Handshake) bool {
	return cfg.Fallback.Enabled && cfg.Fallback.Status.Enabled && handshake.NextState == mcproto.NextStateStatus
}

func loginFallbackForRouteDeniedEnabled(cfg config.Config, handshake mcproto.Handshake) bool {
	if !loginFallbackEnabled(cfg, handshake) {
		return false
	}
	return cfg.Fallback.Login.RespondOnRouteDenied == nil || *cfg.Fallback.Login.RespondOnRouteDenied
}

func loginFallbackEnabled(cfg config.Config, handshake mcproto.Handshake) bool {
	return cfg.Fallback.Enabled && cfg.Fallback.Login.Enabled && handshake.NextState == mcproto.NextStateLogin
}

func (s *Server) serveStatusOverride(client net.Conn, handshakeTimeout time.Duration, override config.StatusOverride) error {
	if err := client.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	packetID, payload, err := mcproto.ReadPacket(client, s.limits.MaxPacketLength)
	if err != nil {
		return err
	}
	if packetID != mcproto.StatusRequestPacketID || len(payload) != 0 {
		return errors.New("malformed status request")
	}

	response, err := mcproto.BuildStatusResponsePacket(mcproto.StatusResponse{
		Version: mcproto.StatusVersion{
			Name:     override.ProtocolName,
			Protocol: override.ProtocolVersion,
		},
		Players: mcproto.StatusPlayers{
			Max:    override.MaxPlayers,
			Online: override.OnlinePlayers,
		},
		Description: mcproto.StatusChatComponent{
			Text: override.MOTD,
		},
	})
	if err != nil {
		return err
	}
	if err := writeAll(client, response); err != nil {
		return err
	}

	if err := client.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	packetID, payload, err = mcproto.ReadPacket(client, s.limits.MaxPacketLength)
	if err != nil {
		if errors.Is(err, io.EOF) || isTimeout(err) {
			return nil
		}
		return err
	}
	if packetID != mcproto.StatusPingPacketID || len(payload) != 8 {
		return errors.New("malformed status ping")
	}
	return writeAll(client, mcproto.BuildStatusPongPacket(payload))
}

func (s *Server) serveStatusFallback(client net.Conn, cfg config.Config, remoteAddr string, routeAddress string, reason string, backendAddress string, connectionAttrs []any) error {
	if err := client.SetReadDeadline(time.Now().Add(cfg.HandshakeTimeout.Duration)); err != nil {
		return err
	}
	packetID, payload, err := mcproto.ReadPacket(client, s.limits.MaxPacketLength)
	if err != nil {
		return err
	}
	if packetID != mcproto.StatusRequestPacketID || len(payload) != 0 {
		return errors.New("malformed status request")
	}

	status := mcproto.StatusResponse{
		Version: mcproto.StatusVersion{
			Name:     cfg.Fallback.Status.ProtocolName,
			Protocol: cfg.Fallback.Status.ProtocolVersion,
		},
		Players: mcproto.StatusPlayers{
			Max:    cfg.Fallback.Status.MaxPlayers,
			Online: cfg.Fallback.Status.OnlinePlayers,
		},
		Description: mcproto.StatusChatComponent{
			Text: cfg.Fallback.Status.MOTD,
		},
	}
	response, err := mcproto.BuildStatusResponsePacket(status)
	if err != nil {
		return err
	}
	if err := writeAll(client, response); err != nil {
		return err
	}
	s.metrics.FallbackResponse(gatewaymetrics.FallbackStateStatus, reason)
	s.logStatusFallbackSent("fallback status response sent", reason, remoteAddr, routeAddress, backendAddress, connectionAttrs)

	if err := client.SetReadDeadline(time.Now().Add(cfg.HandshakeTimeout.Duration)); err != nil {
		return err
	}
	packetID, payload, err = mcproto.ReadPacket(client, s.limits.MaxPacketLength)
	if err != nil {
		if errors.Is(err, io.EOF) || isTimeout(err) {
			return nil
		}
		return err
	}
	if packetID != mcproto.StatusPingPacketID || len(payload) != 8 {
		return errors.New("malformed status ping")
	}
	if err := writeAll(client, mcproto.BuildStatusPongPacket(payload)); err != nil {
		return err
	}
	s.logStatusFallbackSent("fallback status pong sent", reason, remoteAddr, routeAddress, backendAddress, connectionAttrs)
	return nil
}

func (s *Server) serveLoginDisconnectFallback(client net.Conn, cfg config.Config, handshake mcproto.Handshake, remoteAddr string, routeAddress string, connectionAttrs []any) error {
	if err := client.SetReadDeadline(time.Now().Add(cfg.HandshakeTimeout.Duration)); err != nil {
		return err
	}
	packetID, payload, err := mcproto.ReadPacket(client, s.limits.MaxPacketLength)
	if err != nil {
		return err
	}
	if packetID != mcproto.LoginStartPacketID {
		return errors.New("malformed login start")
	}
	if err := mcproto.ValidateLoginStartPayload(handshake.ProtocolVersion, payload); err != nil {
		return err
	}
	response, err := mcproto.BuildLoginDisconnectPacket(handshake.ProtocolVersion, cfg.Fallback.Login.Message)
	if err != nil {
		return err
	}
	if err := writeAll(client, response); err != nil {
		return err
	}
	s.metrics.FallbackResponse(gatewaymetrics.FallbackStateLogin, reasonRouteDenied)
	s.logFallbackSent("fallback login disconnect sent", gatewaymetrics.FallbackStateLogin, reasonRouteDenied, remoteAddr, routeAddress, "", connectionAttrs)
	return nil
}

func (s *Server) logStatusFallbackSent(message string, reason string, remoteAddr string, routeAddress string, backendAddress string, connectionAttrs []any) {
	s.logFallbackSent(message, gatewaymetrics.FallbackStateStatus, reason, remoteAddr, routeAddress, backendAddress, connectionAttrs)
}

func (s *Server) logFallbackSent(message string, state string, reason string, remoteAddr string, routeAddress string, backendAddress string, connectionAttrs []any) {
	attrs := append([]any(nil), connectionAttrs...)
	attrs = append(attrs, "reason", reason, "state", state, "server_address", routeAddress)
	if backendAddress != "" {
		attrs = append(attrs, "backend", backendAddress)
	}
	s.logger.Info(message, attrs...)
}

type proxyResult struct {
	reason      string
	direction   string
	bytesCopied int64
}

func (s *Server) proxy(ctx context.Context, client net.Conn, backend net.Conn) proxyResult {
	done := make(chan proxyResult, 2)
	go copyAndClose(done, "client_to_backend", backend, client)
	go copyAndClose(done, "backend_to_client", client, backend)

	remaining := 2
	result := proxyResult{reason: reasonContextCancelled}
	select {
	case <-ctx.Done():
	case result = <-done:
		remaining--
	}
	_ = client.Close()
	_ = backend.Close()

	for remaining > 0 {
		<-done
		remaining--
	}
	return result
}

func copyAndClose(done chan<- proxyResult, direction string, dst net.Conn, src net.Conn) {
	n, _ := io.Copy(dst, src)
	closeWrite(dst)
	done <- proxyResult{
		reason:      closeReasonForDirection(direction),
		direction:   direction,
		bytesCopied: n,
	}
}

func writeAll(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func classifyDialError(err error) string {
	if errors.Is(err, context.Canceled) {
		return reasonContextCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return reasonBackendDialTimeout
	}
	return reasonBackendDialFailed
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func closeReasonForDirection(direction string) string {
	if direction == "backend_to_client" {
		return reasonBackendClose
	}
	return reasonClientClose
}
