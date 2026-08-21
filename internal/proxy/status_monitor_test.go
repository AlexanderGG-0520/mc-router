package proxy

import (
	"bufio"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
)

func TestObservedStatusReturnsOnlyCompletedFreshSourceResponse(t *testing.T) {
	backend := startStatusProtocolBackend(t)
	defer backend.close()

	gatewayAddr, server, stop := startTestServerWithServer(t, observedStatusTestConfig(backend.addr))
	defer stop()
	first := requestObservedRouterStatus(t, gatewayAddr, "smp.example.com")
	if first.Description.Text != "Backend degraded" {
		t.Fatalf("initial status motd = %q, want degraded", first.Description.Text)
	}
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
	_, _, maxAge := effectiveStatusTiming(cfg.Status)
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
	_ = requestObservedRouterStatus(t, gatewayAddr, "smp.example.com") // Activates the worker once.
	waitClosed(t, probed, "initial source probe did not run")
	waitClosed(t, probed, "source did not probe again without another public STATUS request")
}

func observedStatusTestConfig(backend string) config.Config {
	cfg := validProxyConfig()
	cfg.Fallback.Status.MOTD = "Backend degraded"
	cfg.Status.ProbeInterval = config.Duration{Duration: time.Hour}
	cfg.Status.ProbeTimeout = config.Duration{Duration: time.Second}
	cfg.Status.MaxObservationAge = config.Duration{Duration: time.Hour + time.Second}
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
		_, _, reason, ok := server.statusMonitor.snapshot(key)
		if ok && reason == wantReason {
			return
		}
		time.Sleep(time.Millisecond)
	}
	_, observedAt, reason, ok := server.statusMonitor.snapshot(key)
	t.Fatalf("source state ok=%v observed_at=%s reason=%q, want reason=%q", ok, observedAt, reason, wantReason)
}
