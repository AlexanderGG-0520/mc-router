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
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
	"github.com/AlexanderGG-0520/mc-router/internal/proxyprotocol"
	"github.com/AlexanderGG-0520/mc-router/internal/ratelimit"
	"github.com/AlexanderGG-0520/mc-router/internal/router"
	"github.com/AlexanderGG-0520/mc-router/internal/scaler"
)

type Server struct {
	state         atomic.Pointer[serverState]
	logger        *slog.Logger
	limits        mcproto.Limits
	metrics       *gatewaymetrics.Recorder
	statusMonitor *statusMonitor

	listenAddress string
	dialContext   dialContextFunc
	generation    atomic.Int64

	listenerMu sync.Mutex
	listener   net.Listener
	wg         sync.WaitGroup

	snapshotMu sync.Mutex
}

type serverState struct {
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
	s.statusMonitor = newStatusMonitor(s)
	s.generation.Store(1)
	s.state.Store(newServerState(staticConfig, cfg, routeTable, snapshot.DiscoveryMerge, snapshot.DiscoveredRoutes))
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
	s.state.Store(newServerState(staticConfig, cfg, routeTable, snapshot.DiscoveryMerge, snapshot.DiscoveredRoutes))
	if s.statusMonitor != nil {
		s.statusMonitor.Reconcile(cfg)
	}
	generation := s.generation.Add(1)
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

func newServerState(staticConfig, cfg config.Config, routeTable *router.Router, merge discovery.MergeResult, discoveredRoutes []kubernetes.DiscoveredRoute) *serverState {
	policy, err := clientpolicy.New(cfg.ClientPolicy.Allow, cfg.ClientPolicy.Deny)
	if err != nil {
		panic("proxy: invalid validated client policy: " + err.Error())
	}
	trustedProxies, err := proxyprotocol.ParseCIDRs(cfg.ProxyProtocol.TrustedProxies)
	if err != nil {
		panic("proxy: invalid validated trusted proxies: " + err.Error())
	}
	return &serverState{
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
	s.statusMonitor.Start(ctx, s.currentState().cfg)
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
	remoteAddr := client.RemoteAddr().String()
	clientAddr, err := addressFromAddr(client.RemoteAddr())
	if err == nil && proxyprotocol.Trusted(state.trustedProxies, clientAddr) {
		clientAddr, err = proxyprotocol.Read(client)
		if err != nil {
			connectionResult = gatewaymetrics.ConnectionResultDenied
			connectionReason = reasonHandshakeMalformed
			s.logger.Warn("connection rejected", "reason", "proxy_protocol_invalid", "remote", remoteAddr)
			return
		}
		remoteAddr = clientAddr.String()
	}
	if err != nil || !state.clientPolicy.Allows(clientAddr) {
		connectionResult = gatewaymetrics.ConnectionResultDenied
		connectionReason = gatewaymetrics.ReasonClientDenied
		if err != nil {
			s.logger.Warn("connection rejected", "reason", connectionReason, "remote", remoteAddr, "error", err)
			return
		}
		s.logger.Info("connection rejected", "reason", connectionReason, "remote", remoteAddr)
		return
	}
	if !state.clientRateLimit.Allow(clientAddr) {
		connectionResult = gatewaymetrics.ConnectionResultDenied
		connectionReason = gatewaymetrics.ReasonRateLimited
		s.logger.Info("connection rejected", "reason", connectionReason, "remote", remoteAddr)
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
		s.logger.Warn("connection rejected", "reason", reason, "remote", remoteAddr, "error", err)
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
		if statusFallbackForRouteDeniedEnabled(state.cfg, handshake) {
			if err := s.serveStatusFallback(client, state.cfg, remoteAddr, routeAddress, reasonRouteDenied, ""); err != nil {
				s.logger.Warn("fallback status response failed", "reason", reasonRouteDenied, "state", "status", "remote", remoteAddr, "server_address", routeAddress, "error", err)
			}
			return
		}
		if loginFallbackForRouteDeniedEnabled(state.cfg, handshake) {
			if err := s.serveLoginDisconnectFallback(client, state.cfg, handshake, remoteAddr, routeAddress); err != nil {
				s.logger.Warn("fallback login disconnect failed", "reason", reasonRouteDenied, "state", "login", "remote", remoteAddr, "server_address", routeAddress, "error", err)
			}
			return
		}
		s.logger.Info("connection rejected", "reason", reasonRouteDenied, "remote", remoteAddr, "server_address", routeAddress, "error", err)
		return
	}
	s.metrics.RouteDecision(routeDecisionResult(selection.MatchedBy))
	if handshake.NextState == mcproto.NextStateStatus && selection.StatusOverride != nil {
		if err := s.serveStatusOverride(client, state.cfg.HandshakeTimeout.Duration, *selection.StatusOverride); err != nil {
			s.logger.Warn("route status override response failed", "remote", remoteAddr, "server_address", routeAddress, "backend", selection.Backend, "error", err)
			return
		}
		connectionResult = gatewaymetrics.ConnectionResultClosed
		connectionReason = gatewaymetrics.ReasonSuccess
		s.logger.Info("route status override response sent", "remote", remoteAddr, "server_address", routeAddress, "backend", selection.Backend)
		return
	}
	if handshake.NextState == mcproto.NextStateStatus && selection.StatusBackend != "" {
		if err := s.serveObservedStatus(client, state.cfg, selection, routeAddress, remoteAddr); err != nil {
			s.logger.Warn("router status response failed", "remote", remoteAddr, "server_address", routeAddress, "error", err)
			return
		}
		connectionResult = gatewaymetrics.ConnectionResultClosed
		connectionReason = gatewaymetrics.ReasonSuccess
		return
	}
	backendAddress := selection.Backend

	if err := state.scalerWebhook.Notify(ctx, scaler.Event{Backend: backendAddress, ServerAddress: routeAddress, NextState: handshake.NextState}); err != nil {
		s.logger.Warn("scaler webhook failed", "backend", backendAddress, "server_address", routeAddress, "error", err)
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
			if err := s.serveStatusFallback(client, state.cfg, remoteAddr, routeAddress, reason, backendAddress); err != nil {
				s.logger.Warn("fallback status response failed", "reason", reason, "state", "status", "remote", remoteAddr, "server_address", routeAddress, "backend", backendAddress, "error", err)
			}
			return
		}
		s.logger.Warn("connection rejected", "reason", reason, "remote", remoteAddr, "server_address", routeAddress, "backend", backendAddress, "error", err)
		return
	}
	s.metrics.BackendDialFinished(gatewaymetrics.ReasonSuccess, gatewaymetrics.ReasonSuccess, time.Since(dialStart))
	defer backend.Close()

	if err := writeAll(backend, rawHandshake); err != nil {
		connectionResult = gatewaymetrics.ConnectionResultFailed
		connectionReason = reasonInitialWriteFailed
		s.logger.Warn("connection rejected", "reason", reasonInitialWriteFailed, "remote", remoteAddr, "server_address", routeAddress, "backend", backendAddress, "error", err)
		return
	}

	s.logger.Info(
		"proxying connection",
		"remote", remoteAddr,
		"server_address", routeAddress,
		"server_port", handshake.ServerPort,
		"next_state", handshake.NextState,
		"backend", backendAddress,
		"matched_by", selection.MatchedBy,
	)
	result := s.proxy(ctx, client, backend)
	connectionResult = gatewaymetrics.ConnectionResultClosed
	connectionReason = result.reason
	s.logger.Info(
		"proxy connection closed",
		"reason", result.reason,
		"remote", remoteAddr,
		"server_address", routeAddress,
		"backend", backendAddress,
		"direction", result.direction,
		"bytes_copied", result.bytesCopied,
	)
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
	return s.serveStatusPacket(client, handshakeTimeout, response, nil)
}

func (s *Server) serveStatusFallback(client net.Conn, cfg config.Config, remoteAddr string, routeAddress string, reason string, backendAddress string) error {
	response, err := degradedStatusPacket(cfg)
	if err != nil {
		return err
	}
	return s.serveStatusPacket(client, cfg.HandshakeTimeout.Duration, response, func() {
		s.metrics.FallbackResponse(gatewaymetrics.FallbackStateStatus, reason)
		s.logStatusFallbackSent("fallback status response sent", reason, remoteAddr, routeAddress, backendAddress)
	})
}

func (s *Server) serveObservedStatus(client net.Conn, cfg config.Config, selection router.Selection, routeAddress, remoteAddr string) error {
	if err := readStatusRequest(client, cfg.HandshakeTimeout.Duration, s.limits); err != nil {
		return err
	}
	backend := selection.Backend
	if selection.StatusBackend != "" {
		backend = selection.StatusBackend
	}
	response, reason := s.statusMonitor.response(statusSourceKey{backend: backend, routeAddress: routeAddress}, cfg.Status)
	if len(response) == 0 {
		var err error
		response, err = degradedStatusPacket(cfg)
		if err != nil {
			return err
		}
		if err := writeAll(client, response); err != nil {
			return err
		}
		s.metrics.FallbackResponse(gatewaymetrics.FallbackStateStatus, reason)
		s.logStatusFallbackSent("degraded status response sent", reason, remoteAddr, routeAddress, backend)
		return serveOptionalStatusPing(client, cfg.HandshakeTimeout.Duration, s.limits)
	}
	if err := writeAll(client, response); err != nil {
		return err
	}
	return serveOptionalStatusPing(client, cfg.HandshakeTimeout.Duration, s.limits)
}

func degradedStatusPacket(cfg config.Config) ([]byte, error) {
	fallback := cfg.Fallback.Status
	if fallback.MOTD == "" && fallback.ProtocolName == "" && fallback.ProtocolVersion == 0 && fallback.MaxPlayers == 0 && fallback.OnlinePlayers == 0 {
		fallback = config.Defaults().Fallback.Status
	}
	return mcproto.BuildStatusResponsePacket(mcproto.StatusResponse{
		Version:     mcproto.StatusVersion{Name: fallback.ProtocolName, Protocol: fallback.ProtocolVersion},
		Players:     mcproto.StatusPlayers{Max: fallback.MaxPlayers, Online: fallback.OnlinePlayers},
		Description: mcproto.StatusChatComponent{Text: fallback.MOTD},
	})
}

func (s *Server) serveStatusPacket(client net.Conn, handshakeTimeout time.Duration, response []byte, afterWrite func()) error {
	if err := readStatusRequest(client, handshakeTimeout, s.limits); err != nil {
		return err
	}
	if err := writeAll(client, response); err != nil {
		return err
	}
	if afterWrite != nil {
		afterWrite()
	}
	return serveOptionalStatusPing(client, handshakeTimeout, s.limits)
}

func readStatusRequest(client net.Conn, handshakeTimeout time.Duration, limits mcproto.Limits) error {
	if err := client.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	packetID, payload, err := mcproto.ReadPacket(client, limits.MaxPacketLength)
	if err != nil {
		return err
	}
	if packetID != mcproto.StatusRequestPacketID || len(payload) != 0 {
		return errors.New("malformed status request")
	}
	return nil
}

func serveOptionalStatusPing(client net.Conn, handshakeTimeout time.Duration, limits mcproto.Limits) error {
	if err := client.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	packetID, payload, err := mcproto.ReadPacket(client, limits.MaxPacketLength)
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

func (s *Server) serveLoginDisconnectFallback(client net.Conn, cfg config.Config, handshake mcproto.Handshake, remoteAddr string, routeAddress string) error {
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
	s.logFallbackSent("fallback login disconnect sent", gatewaymetrics.FallbackStateLogin, reasonRouteDenied, remoteAddr, routeAddress, "")
	return nil
}

func (s *Server) logStatusFallbackSent(message string, reason string, remoteAddr string, routeAddress string, backendAddress string) {
	s.logFallbackSent(message, gatewaymetrics.FallbackStateStatus, reason, remoteAddr, routeAddress, backendAddress)
}

func (s *Server) logFallbackSent(message string, state string, reason string, remoteAddr string, routeAddress string, backendAddress string) {
	attrs := []any{"reason", reason, "state", state, "remote", remoteAddr, "server_address", routeAddress}
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
