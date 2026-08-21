package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
)

// statusMonitor separates source observations from public STATUS handling.
// Workers maintain a health state from an observation sequence; public
// requests only read that state and never wait for a source request.
type statusMonitor struct {
	server *Server

	mu      sync.Mutex
	ctx     context.Context
	started bool
	entries map[statusSourceKey]*statusSource
	wg      sync.WaitGroup
}

type statusSourceKey struct {
	backend      string
	routeAddress string
}

type statusSource struct {
	key statusSourceKey

	cancel context.CancelFunc
	poke   chan struct{}

	response             []byte
	observedAt           time.Time
	reason               string
	health               statusHealth
	consecutiveFailures  int
	consecutiveSuccesses int
	timing               config.Status
}

type statusHealth string

const (
	statusHealthUnknown  statusHealth = "unknown"
	statusHealthNormal   statusHealth = "normal"
	statusHealthDegraded statusHealth = "degraded"
)

func newStatusMonitor(server *Server) *statusMonitor {
	return &statusMonitor{server: server, entries: make(map[statusSourceKey]*statusSource)}
}

func (m *statusMonitor) Start(ctx context.Context, cfg config.Config) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.ctx = ctx
	m.started = true
	// A configured source has a router-owned state from process start. Login and
	// Transfer never use those states, but public STATUS is not responsible for
	// initiating the observation that it reports.
	m.reconcileExistingLocked(cfg)
	m.mu.Unlock()
}

func (m *statusMonitor) Reconcile(cfg config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return
	}
	m.reconcileExistingLocked(cfg)
}

func (m *statusMonitor) reconcileExistingLocked(cfg config.Config) {
	desired := statusSourceDefinitions(cfg)
	for key, source := range m.entries {
		if _, ok := desired[key]; ok {
			source.timing = cfg.Status
			select {
			case source.poke <- struct{}{}:
			default:
			}
			delete(desired, key)
			continue
		}
		source.cancel()
		delete(m.entries, key)
	}
	for key := range desired {
		m.startSourceLocked(key, cfg.Status)
	}
}

func statusSourceDefinitions(cfg config.Config) map[statusSourceKey]struct{} {
	result := make(map[statusSourceKey]struct{}, len(cfg.Routes))
	for _, route := range cfg.Routes {
		// statusBackend is the explicit opt-in to router-owned STATUS
		// observations. Routes without it retain their established transparent
		// STATUS proxy behaviour and must not acquire background probes.
		if route.StatusOverride != nil || route.StatusBackend == "" {
			continue
		}
		routeAddress, err := hostaddr.Normalize(route.ServerAddress)
		if err != nil {
			continue // Config was validated before reaching runtime.
		}
		result[statusSourceKey{backend: route.StatusBackend, routeAddress: routeAddress}] = struct{}{}
	}
	return result
}

func (m *statusMonitor) sourceFor(key statusSourceKey, timing config.Status) *statusSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	source := m.entries[key]
	if source != nil || !m.started {
		return source
	}
	// defaultRoute and routes published concurrently with a snapshot reload can
	// reach here before reconciliation has created their worker. Starting one
	// still creates an UNKNOWN state first; the public request never probes.
	m.startSourceLocked(key, timing)
	return m.entries[key]
}

func (m *statusMonitor) startSourceLocked(key statusSourceKey, timing config.Status) {
	ctx, cancel := context.WithCancel(m.ctx)
	source := &statusSource{
		key:    key,
		cancel: cancel,
		poke:   make(chan struct{}, 1),
		reason: gatewaymetrics.ReasonBackendStatusUnknown,
		health: statusHealthUnknown,
		timing: timing,
	}
	m.entries[key] = source
	m.wg.Add(1)
	go m.runSource(ctx, source)
}

func (m *statusMonitor) runSource(ctx context.Context, source *statusSource) {
	defer m.wg.Done()
	for {
		m.probe(ctx, source)
		interval, _, _, _, _ := effectiveStatusPolicy(m.timing(source))
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-source.poke:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (m *statusMonitor) probe(parent context.Context, source *statusSource) {
	_, timeout, _, _, _ := effectiveStatusPolicy(m.timing(source))
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	response, reason := m.fetch(ctx, source.key)
	if parent.Err() != nil {
		return
	}

	m.mu.Lock()
	current := m.entries[source.key]
	if current == source {
		current.applyObservation(response, reason, time.Now())
	}
	m.mu.Unlock()
	m.server.metrics.StatusSourceProbe(statusProbeResult(reason), reason)
}

func (source *statusSource) applyObservation(response []byte, reason string, observedAt time.Time) {
	source.observedAt = observedAt
	source.reason = reason
	if reason == gatewaymetrics.ReasonSuccess {
		source.response = response
		source.consecutiveSuccesses++
		source.consecutiveFailures = 0
		_, _, _, _, recoveryThreshold := effectiveStatusPolicy(source.timing)
		if source.health != statusHealthNormal && source.consecutiveSuccesses >= recoveryThreshold {
			source.health = statusHealthNormal
		}
		return
	}
	source.consecutiveFailures++
	source.consecutiveSuccesses = 0
	_, _, _, failureThreshold, _ := effectiveStatusPolicy(source.timing)
	if source.health == statusHealthNormal && source.consecutiveFailures >= failureThreshold {
		source.health = statusHealthDegraded
	}
}

func (m *statusMonitor) fetch(ctx context.Context, key statusSourceKey) ([]byte, string) {
	conn, err := m.server.dialContext(ctx, "tcp", key.backend)
	if err != nil {
		return nil, classifyStatusProbeError(err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, gatewaymetrics.ReasonBackendStatusFailed
		}
	}
	if err := writeAll(conn, buildStatusProbeHandshake(key)); err != nil {
		return nil, classifyStatusProbeError(err)
	}
	if err := writeAll(conn, mcproto.BuildPacket(mcproto.StatusRequestPacketID)); err != nil {
		return nil, classifyStatusProbeError(err)
	}
	packetID, payload, err := mcproto.ReadPacket(conn, m.server.limits.MaxPacketLength)
	if err != nil {
		return nil, classifyStatusProbeError(err)
	}
	if packetID != mcproto.StatusResponsePacketID || mcproto.ValidateStatusResponsePayload(payload) != nil {
		return nil, gatewaymetrics.ReasonBackendStatusInvalid
	}
	return mcproto.BuildPacket(packetID, payload), gatewaymetrics.ReasonSuccess
}

func (m *statusMonitor) response(key statusSourceKey, timing config.Status) ([]byte, string) {
	source := m.sourceFor(key, timing)
	if source == nil {
		return nil, gatewaymetrics.ReasonBackendStatusUnknown
	}
	_, _, maxAge, _, _ := effectiveStatusPolicy(m.timing(source))
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if source.observedAt.IsZero() || now.Sub(source.observedAt) > maxAge {
		// The observation loop has exceeded its own liveness bound. Its prior
		// sequence cannot be used to recover NORMAL after it resumes.
		source.health = statusHealthUnknown
		source.consecutiveFailures = 0
		source.consecutiveSuccesses = 0
		return nil, gatewaymetrics.ReasonBackendStatusStale
	}
	if source.health != statusHealthNormal || len(source.response) == 0 {
		if source.health == statusHealthUnknown && source.reason == gatewaymetrics.ReasonSuccess {
			return nil, gatewaymetrics.ReasonBackendStatusRecovering
		}
		return nil, source.reason
	}
	return append([]byte(nil), source.response...), gatewaymetrics.ReasonSuccess
}

func (m *statusMonitor) timing(source *statusSource) config.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return source.timing
}

func (m *statusMonitor) Wait() {
	m.wg.Wait()
}

func effectiveStatusPolicy(status config.Status) (time.Duration, time.Duration, time.Duration, int, int) {
	defaults := config.Defaults().Status
	if status.ProbeInterval.Duration <= 0 {
		status.ProbeInterval = defaults.ProbeInterval
	}
	if status.ProbeTimeout.Duration <= 0 {
		status.ProbeTimeout = defaults.ProbeTimeout
	}
	if status.FailureThreshold <= 0 {
		status.FailureThreshold = defaults.FailureThreshold
	}
	if status.RecoveryThreshold <= 0 {
		status.RecoveryThreshold = defaults.RecoveryThreshold
	}
	if status.MaxObservationAge.Duration <= 0 {
		status.MaxObservationAge = defaults.MaxObservationAge
	}
	return status.ProbeInterval.Duration, status.ProbeTimeout.Duration, status.MaxObservationAge.Duration, status.FailureThreshold, status.RecoveryThreshold
}

func buildStatusProbeHandshake(key statusSourceKey) []byte {
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], 25565)
	return mcproto.BuildPacket(
		mcproto.HandshakePacketID,
		mcproto.WriteVarInt(767),
		mcproto.EncodeString(key.routeAddress),
		port[:],
		mcproto.WriteVarInt(mcproto.NextStateStatus),
	)
}

func classifyStatusProbeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return gatewaymetrics.ReasonBackendStatusTimeout
	}
	return gatewaymetrics.ReasonBackendStatusFailed
}

func statusProbeResult(reason string) string {
	if reason == gatewaymetrics.ReasonSuccess {
		return gatewaymetrics.ConnectionResultAccepted
	}
	return gatewaymetrics.ConnectionResultFailed
}

func (m *statusMonitor) snapshot(key statusSourceKey) (response []byte, observedAt time.Time, reason string, health statusHealth, consecutiveFailures, consecutiveSuccesses int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	source := m.entries[key]
	if source == nil {
		return nil, time.Time{}, "", statusHealthUnknown, 0, 0, false
	}
	return append([]byte(nil), source.response...), source.observedAt, source.reason, source.health, source.consecutiveFailures, source.consecutiveSuccesses, true
}
