package proxy

import (
	"bufio"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
)

func TestObservedStatusReturnsOnlyCompletedHealthySourceResponse(t *testing.T) {
	backend := startStatusProtocolBackend(t)
	defer backend.close()

	gatewayAddr, server, stop := startTestServerWithServer(t, observedStatusTestConfig(backend.addr))
	defer stop()
	result := waitProtocolResult(t, backend.result)
	if result.handshake.NextState != mcproto.NextStateStatus || result.handshake.ServerAddress != "smp.example.com" {
		t.Fatalf("source handshake = %#v", result.handshake)
	}
	waitObservedSource(t, server, observedSourceKey(backend.addr), gatewaymetrics.ReasonSuccess)

	status := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	if status.Version.Name != "mc-router-smoke" || status.Description.Text != "mc-router smoke" {
		t.Fatalf("status = %#v, want completed source response", status)
	}
}

func TestObservedStatusTCPFailureReturnsDegradedResponse(t *testing.T) {
	listener := listenLocalTCP(t)
	backendAddress := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close source listener: %v", err)
	}
	gatewayAddr, server, stop := startTestServerWithServer(t, observedStatusTestConfig(backendAddress))
	defer stop()
	status := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	if status.Description.Text != "Backend degraded" {
		t.Fatalf("degraded motd = %q", status.Description.Text)
	}
	waitObservedSource(t, server, observedSourceKey(backendAddress), gatewaymetrics.ReasonBackendStatusFailed)
}

func TestObservedStatusTimeoutDoesNotDelayClientResponse(t *testing.T) {
	listener := listenLocalTCP(t)
	defer listener.Close()
	accepted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, _, err := mcproto.ReadHandshake(reader, mcproto.DefaultLimits()); err != nil {
			return
		}
		if id, payload, err := mcproto.ReadPacket(reader, mcproto.DefaultLimits().MaxPacketLength); err != nil || id != mcproto.StatusRequestPacketID || len(payload) != 0 {
			return
		}
		close(accepted)
		<-release
	}()

	cfg := observedStatusTestConfig(listener.Addr().String())
	cfg.Status.ProbeInterval = config.Duration{Duration: 50 * time.Millisecond}
	cfg.Status.ProbeTimeout = config.Duration{Duration: 250 * time.Millisecond}
	cfg.Status.MaxObservationAge = config.Duration{Duration: 300 * time.Millisecond}
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	started := time.Now()
	status := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("public status took %s and waited for source", elapsed)
	}
	if status.Description.Text != "Backend degraded" {
		t.Fatalf("degraded motd = %q", status.Description.Text)
	}
	waitClosed(t, accepted, "status source was not queried")
	waitObservedSource(t, server, observedSourceKey(listener.Addr().String()), gatewaymetrics.ReasonBackendStatusTimeout)
}

func TestObservedStatusFallsBackAfterHealthySourceStalls(t *testing.T) {
	listener := listenLocalTCP(t)
	defer listener.Close()
	stalled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		for attempt := 0; ; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			reader := bufio.NewReader(conn)
			_, _, handshakeErr := mcproto.ReadHandshake(reader, mcproto.DefaultLimits())
			packetID, payload, requestErr := mcproto.ReadPacket(reader, mcproto.DefaultLimits().MaxPacketLength)
			if handshakeErr != nil || requestErr != nil || packetID != mcproto.StatusRequestPacketID || len(payload) != 0 {
				_ = conn.Close()
				return
			}
			if attempt == 0 {
				response, _ := mcproto.BuildStatusResponsePacket(mcproto.StatusResponse{
					Version:     mcproto.StatusVersion{Name: "source", Protocol: 767},
					Players:     mcproto.StatusPlayers{},
					Description: mcproto.StatusChatComponent{Text: "source healthy"},
				})
				_ = writeAll(conn, response)
				_ = conn.Close()
				continue
			}
			close(stalled)
			<-release
			_ = conn.Close()
			return
		}
	}()

	cfg := observedStatusTestConfig(listener.Addr().String())
	cfg.Status.ProbeInterval = config.Duration{Duration: 25 * time.Millisecond}
	cfg.Status.ProbeTimeout = config.Duration{Duration: 80 * time.Millisecond}
	cfg.Status.MaxObservationAge = config.Duration{Duration: 105 * time.Millisecond}
	cfg.Status.FailureThreshold = 1
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()
	key := observedSourceKey(listener.Addr().String())
	waitObservedSource(t, server, key, gatewaymetrics.ReasonSuccess)
	if status := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com"); status.Description.Text != "source healthy" {
		t.Fatalf("healthy status motd = %q", status.Description.Text)
	}

	waitClosed(t, stalled, "source did not enter STATUS stall")
	waitObservedSource(t, server, key, gatewaymetrics.ReasonBackendStatusTimeout)
	started := time.Now()
	status := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	if elapsed := time.Since(started); elapsed >= 50*time.Millisecond {
		t.Fatalf("degraded status took %s and waited for source", elapsed)
	}
	if status.Description.Text != "Backend degraded" {
		t.Fatalf("degraded motd = %q", status.Description.Text)
	}
}

func TestObservedStatusRejectsMalformedSourceResponse(t *testing.T) {
	listener := listenLocalTCP(t)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, _, err := mcproto.ReadHandshake(reader, mcproto.DefaultLimits()); err != nil {
			return
		}
		if _, _, err := mcproto.ReadPacket(reader, mcproto.DefaultLimits().MaxPacketLength); err != nil {
			return
		}
		_ = writeAll(conn, mcproto.BuildPacket(mcproto.StatusResponsePacketID, mcproto.EncodeString(`{"invalid":`)))
	}()

	gatewayAddr, server, stop := startTestServerWithServer(t, observedStatusTestConfig(listener.Addr().String()))
	defer stop()
	status := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	if status.Description.Text != "Backend degraded" {
		t.Fatalf("degraded motd = %q", status.Description.Text)
	}
	waitObservedSource(t, server, observedSourceKey(listener.Addr().String()), gatewaymetrics.ReasonBackendStatusInvalid)
}

func TestObservedStatusExpiresAFormerlyHealthyResponse(t *testing.T) {
	backend := startStatusProtocolBackend(t)
	defer backend.close()
	cfg := observedStatusTestConfig(backend.addr)
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()
	_ = requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	_ = waitProtocolResult(t, backend.result)
	key := observedSourceKey(backend.addr)
	waitObservedSource(t, server, key, gatewaymetrics.ReasonSuccess)

	server.statusMonitor.mu.Lock()
	source := server.statusMonitor.entries[key]
	_, _, maxAge, _, _ := effectiveStatusPolicy(cfg.Status)
	source.observedAt = time.Now().Add(-maxAge - time.Nanosecond)
	server.statusMonitor.mu.Unlock()

	status := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	if status.Description.Text != "Backend degraded" {
		t.Fatalf("stale source motd = %q, want degraded", status.Description.Text)
	}
}

func TestStatusSourceContinuesProbingWithoutFurtherPublicRequests(t *testing.T) {
	listener := listenLocalTCP(t)
	defer listener.Close()
	probed := make(chan struct{}, 2)
	go func() {
		for range 2 {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			reader := bufio.NewReader(conn)
			_, _, handshakeErr := mcproto.ReadHandshake(reader, mcproto.DefaultLimits())
			packetID, payload, requestErr := mcproto.ReadPacket(reader, mcproto.DefaultLimits().MaxPacketLength)
			if handshakeErr == nil && requestErr == nil && packetID == mcproto.StatusRequestPacketID && len(payload) == 0 {
				response, _ := mcproto.BuildStatusResponsePacket(mcproto.StatusResponse{
					Version:     mcproto.StatusVersion{Name: "source", Protocol: 767},
					Players:     mcproto.StatusPlayers{},
					Description: mcproto.StatusChatComponent{Text: "source"},
				})
				_ = writeAll(conn, response)
				probed <- struct{}{}
			}
			_ = conn.Close()
		}
	}()

	cfg := observedStatusTestConfig(listener.Addr().String())
	cfg.Status.ProbeInterval = config.Duration{Duration: 25 * time.Millisecond}
	cfg.Status.ProbeTimeout = config.Duration{Duration: 25 * time.Millisecond}
	cfg.Status.MaxObservationAge = config.Duration{Duration: 50 * time.Millisecond}
	gatewayAddr, _, stop := startTestServerWithServer(t, cfg)
	defer stop()
	waitClosed(t, probed, "initial source probe did not run")
	waitClosed(t, probed, "source did not probe again without another public STATUS request")
}

func observedStatusTestConfig(backend string) config.Config {
	cfg := validProxyConfig()
	cfg.Fallback.Status.MOTD = "Backend degraded"
	cfg.Status.ProbeInterval = config.Duration{Duration: time.Hour}
	cfg.Status.ProbeTimeout = config.Duration{Duration: time.Second}
	cfg.Status.MaxObservationAge = config.Duration{Duration: time.Hour + time.Second}
	cfg.Status.RecoveryThreshold = 1
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: "127.0.0.1:1", StatusBackend: backend}}
	return cfg
}

func requestObservedRouterStatus(t *testing.T, gatewayAddr, host string) mcproto.StatusResponse {
	t.Helper()
	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, host, 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer conn.Close()
	return readStatusResponse(t, conn)
}

func observedSourceKey(backend string) statusSourceKey {
	return statusSourceKey{backend: backend, routeAddress: "smp.example.com"}
}

func waitObservedSource(t *testing.T, server *Server, key statusSourceKey, wantReason string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, _, reason, _, _, _, ok := server.statusMonitor.snapshot(key)
		if ok && reason == wantReason {
			return
		}
		time.Sleep(time.Millisecond)
	}
	_, observedAt, reason, health, failures, successes, ok := server.statusMonitor.snapshot(key)
	t.Fatalf("source state ok=%v observed_at=%s reason=%q health=%q failures=%d successes=%d, want reason=%q", ok, observedAt, reason, health, failures, successes, wantReason)
}

func TestStatusHealthUsesConsecutiveFailureAndRecoveryThresholds(t *testing.T) {
	key := statusSourceKey{backend: "backend:25565", routeAddress: "smp.example.com"}
	source := &statusSource{
		key:    key,
		timing: config.Status{FailureThreshold: 3, RecoveryThreshold: 2, MaxObservationAge: config.Duration{Duration: time.Hour}},
		health: statusHealthUnknown,
		reason: gatewaymetrics.ReasonBackendStatusUnknown,
	}
	monitor := &statusMonitor{entries: map[statusSourceKey]*statusSource{key: source}}
	now := time.Now()
	source.applyObservation([]byte("first"), gatewaymetrics.ReasonSuccess, now)
	if source.health != statusHealthUnknown || source.consecutiveSuccesses != 1 {
		t.Fatalf("after first success: health=%q successes=%d", source.health, source.consecutiveSuccesses)
	}
	if response, reason := monitor.response(key, source.timing); response != nil || reason != gatewaymetrics.ReasonBackendStatusRecovering {
		t.Fatalf("before recovery threshold: response=%q reason=%q", response, reason)
	}
	source.applyObservation([]byte("second"), gatewaymetrics.ReasonSuccess, now)
	if source.health != statusHealthNormal || source.consecutiveSuccesses != 2 {
		t.Fatalf("after recovery threshold: health=%q successes=%d", source.health, source.consecutiveSuccesses)
	}
	if response, _ := monitor.response(key, source.timing); string(response) != "second" {
		t.Fatalf("normal response = %q, want second", response)
	}
	for failure := 1; failure <= 2; failure++ {
		source.applyObservation(nil, gatewaymetrics.ReasonBackendStatusTimeout, now)
		if source.health != statusHealthNormal || source.consecutiveFailures != failure {
			t.Fatalf("after failure %d: health=%q failures=%d", failure, source.health, source.consecutiveFailures)
		}
		if response, _ := monitor.response(key, source.timing); string(response) != "second" {
			t.Fatalf("response after transient failure %d = %q, want second", failure, response)
		}
	}
	source.applyObservation(nil, gatewaymetrics.ReasonBackendStatusTimeout, now)
	if source.health != statusHealthDegraded || source.consecutiveFailures != 3 {
		t.Fatalf("after failure threshold: health=%q failures=%d", source.health, source.consecutiveFailures)
	}
	if response, _ := monitor.response(key, source.timing); response != nil {
		t.Fatalf("response after failure threshold = %q, want degraded fallback", response)
	}
	source.applyObservation([]byte("third"), gatewaymetrics.ReasonSuccess, now)
	if source.health != statusHealthDegraded || source.consecutiveSuccesses != 1 {
		t.Fatalf("after first recovery success: health=%q successes=%d", source.health, source.consecutiveSuccesses)
	}
	source.applyObservation([]byte("fourth"), gatewaymetrics.ReasonSuccess, now)
	if source.health != statusHealthNormal || source.consecutiveSuccesses != 2 {
		t.Fatalf("after recovery threshold again: health=%q successes=%d", source.health, source.consecutiveSuccesses)
	}
}
