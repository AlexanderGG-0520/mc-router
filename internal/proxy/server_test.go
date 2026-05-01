package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	"github.com/AlexanderGG-0520/mc-router/internal/router"
	dto "github.com/prometheus/client_model/go"
)

func TestProxyForwardsHandshakeAndRemainingBytesToKnownBackend(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendBytes := acceptAndReadOnce(t, backendListener)

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})
	defer stop()

	handshake := buildHandshakePacket(765, "SMP.Example.COM.", 25565, mcproto.NextStateLogin)
	remaining := []byte{0x01, 0x02, 0x03, 0x04}
	client := dialAndWrite(t, gatewayAddr, append(append([]byte{}, handshake...), remaining...))
	defer client.Close()
	closeClientWrite(t, client)

	got := waitBytes(t, backendBytes)
	want := append(append([]byte{}, handshake...), remaining...)
	if !bytes.Equal(got, want) {
		t.Fatalf("backend bytes = %v, want %v", got, want)
	}
}

func TestProxyDeniesUnknownHostWithoutConnectingBackend(t *testing.T) {
	dialed := make(chan struct{}, 1)

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: "127.0.0.1:1"},
		},
	}, func(server *Server) {
		server.dialContext = func(context.Context, string, string) (net.Conn, error) {
			dialed <- struct{}{}
			return nil, errors.New("dial should not be called")
		}
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	closeClientWrite(t, client)
	readClosed(t, client)

	select {
	case <-dialed:
		t.Fatal("dialer was called for an unknown host")
	default:
	}
}

func TestStatusFallbackDisabledDeniesUnknownHostWithClose(t *testing.T) {
	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: "127.0.0.1:1"},
		},
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer client.Close()
	readClosed(t, client)
}

func TestStatusFallbackRespondsForRouteDeniedStatusPing(t *testing.T) {
	dialed := make(chan struct{}, 1)
	cfg := statusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg, func(server *Server) {
		server.dialContext = func(context.Context, string, string) (net.Conn, error) {
			dialed <- struct{}{}
			return nil, errors.New("dial should not be called")
		}
	})
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer conn.Close()

	status := readStatusResponse(t, conn)
	if status.Version.Name != "mc-gateway" {
		t.Fatalf("status version name = %q", status.Version.Name)
	}
	if status.Version.Protocol != 767 {
		t.Fatalf("status protocol = %d", status.Version.Protocol)
	}
	if status.Players.Max != 10 {
		t.Fatalf("status max players = %d", status.Players.Max)
	}
	if status.Players.Online != 2 {
		t.Fatalf("status online players = %d", status.Players.Online)
	}
	if status.Description.Text != `Server "unavailable"` {
		t.Fatalf("status motd = %q", status.Description.Text)
	}

	const pingPayload int64 = 0x1122334455667788
	if err := writeAll(conn, mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(pingPayload))); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	packetID, payload, err := mcproto.ReadPacket(conn, mcproto.DefaultLimits().MaxPacketLength)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if packetID != mcproto.StatusPongPacketID {
		t.Fatalf("pong packet id = %d, want %d", packetID, mcproto.StatusPongPacketID)
	}
	if got := parseLongPayload(t, payload); got != pingPayload {
		t.Fatalf("pong payload = %x, want %x", got, pingPayload)
	}

	select {
	case <-dialed:
		t.Fatal("dialer was called for status fallback")
	default:
	}
	waitMetricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "denied"}, 1)
}

func TestStatusFallbackAllowsClientToSkipPing(t *testing.T) {
	cfg := statusFallbackConfig()
	cfg.HandshakeTimeout = config.Duration{Duration: 50 * time.Millisecond}
	gatewayAddr, stop := startTestServer(t, cfg)
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer conn.Close()
	_ = readStatusResponse(t, conn)
	closeClientWrite(t, conn)
	readClosed(t, conn)
}

func TestStatusFallbackDoesNotHandleLoginRouteDenied(t *testing.T) {
	gatewayAddr, stop := startTestServer(t, statusFallbackConfig())
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	readClosed(t, client)
}

func TestStatusFallbackDoesNotOverrideDefaultRoute(t *testing.T) {
	defaultBackend := listenLocalTCP(t)
	defer defaultBackend.Close()
	backendBytes := acceptAndReadOnce(t, defaultBackend)

	cfg := statusFallbackConfig()
	cfg.UnknownHostPolicy = config.UnknownHostDefault
	cfg.DefaultRoute = config.DefaultRoute{
		Backend: defaultBackend.Addr().String(),
		Mode:    config.RouteModeAllow,
	}
	cfg.Routes = nil
	gatewayAddr, stop := startTestServer(t, cfg)
	defer stop()

	handshake := buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus)
	statusRequest := mcproto.BuildPacket(mcproto.StatusRequestPacketID)
	client := dialAndWrite(t, gatewayAddr, append(append([]byte{}, handshake...), statusRequest...))
	defer client.Close()
	closeClientWrite(t, client)

	if got := waitBytes(t, backendBytes); !bytes.Equal(got, append(append([]byte{}, handshake...), statusRequest...)) {
		t.Fatalf("default backend bytes = %v, want handshake plus status request", got)
	}
}

func TestStatusFallbackClosesMalformedStatusRequest(t *testing.T) {
	gatewayAddr, stop := startTestServer(t, statusFallbackConfig())
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(1))...,
	))
	defer client.Close()
	readClosed(t, client)
}

func TestProxyUsesDefaultBackendForUnknownHostWhenConfigured(t *testing.T) {
	defaultBackend := listenLocalTCP(t)
	defer defaultBackend.Close()
	backendBytes := acceptAndReadOnce(t, defaultBackend)

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDefault,
		DefaultRoute: config.DefaultRoute{
			Backend: defaultBackend.Addr().String(),
			Mode:    config.RouteModeAllow,
		},
	})
	defer stop()

	handshake := buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateLogin)
	client := dialAndWrite(t, gatewayAddr, handshake)
	defer client.Close()
	closeClientWrite(t, client)

	if got := waitBytes(t, backendBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("default backend bytes = %v, want %v", got, handshake)
	}
}

func TestProxyRejectsMalformedHandshakeWithoutConnectingBackend(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDefault,
		DefaultRoute: config.DefaultRoute{
			Backend: backendListener.Addr().String(),
			Mode:    config.RouteModeAllow,
		},
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x00})
	defer client.Close()
	closeClientWrite(t, client)

	tcpListener := backendListener.(*net.TCPListener)
	if err := tcpListener.SetDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	conn, err := backendListener.Accept()
	if err == nil {
		_ = conn.Close()
		t.Fatal("backend accepted a connection for a malformed handshake")
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("backend Accept error = %v, want timeout", err)
	}
}

func TestProxyHandshakeReadTimeoutClosesIdleClient(t *testing.T) {
	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: 30 * time.Millisecond},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
	})
	defer stop()

	client, err := net.DialTimeout("tcp", gatewayAddr, time.Second)
	if err != nil {
		t.Fatalf("DialTimeout: %v", err)
	}
	defer client.Close()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, err = client.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected idle client connection to close")
	}
}

func TestProxyExitsWhenBackendClosesAfterHandshake(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	accepted := make(chan []byte, 1)
	go func() {
		conn, err := backendListener.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		defer conn.Close()
		handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
		buf := make([]byte, len(handshake))
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = io.ReadFull(conn, buf)
		accepted <- buf
	}()

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})
	defer stop()

	handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
	client := dialAndWrite(t, gatewayAddr, handshake)
	defer client.Close()
	if got := waitBytes(t, accepted); !bytes.Equal(got, handshake) {
		t.Fatalf("backend handshake = %v, want %v", got, handshake)
	}
	readClosed(t, client)
}

func TestProxyExitsWhenClientClosesAfterHandshake(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendBytes := acceptAndReadOnce(t, backendListener)

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})
	defer stop()

	handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
	client := dialAndWrite(t, gatewayAddr, handshake)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := waitBytes(t, backendBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("backend bytes = %v, want %v", got, handshake)
	}
}

func TestProxyClosesClientWhenBackendDialFails(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendAddr},
		},
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	readClosed(t, client)
}

func TestProxyBackendDialTimeoutClosesClient(t *testing.T) {
	dialStarted := make(chan struct{})
	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: 40 * time.Millisecond},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: "127.0.0.1:1"},
		},
	}, func(server *Server) {
		server.dialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
			close(dialStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	waitClosed(t, dialStarted, "dialer was not called")
	readClosed(t, client)
}

func TestProxyContextCancellationClosesActiveConnection(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendAccepted := make(chan struct{})
	go func() {
		conn, err := backendListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(backendAccepted)
		_, _ = io.Copy(io.Discard, conn)
	}()

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	waitClosed(t, backendAccepted, "backend was not connected")
	stop()
	readClosed(t, client)
}

func TestMetricsActiveConnectionsGaugeIncrementsAndDecrements(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendAccepted := make(chan net.Conn, 1)
	go func() {
		conn, err := backendListener.Accept()
		if err != nil {
			backendAccepted <- nil
			return
		}
		backendAccepted <- conn
	}()

	gatewayAddr, server, stop := startTestServerWithServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		Metrics:            testMetricsConfig(),
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin))
	backend := waitConn(t, backendAccepted)
	waitMetricValue(t, server, "mc_gateway_active_connections", nil, 1)

	_ = backend.Close()
	readClosed(t, client)
	_ = client.Close()
	waitMetricValue(t, server, "mc_gateway_active_connections", nil, 0)
}

func TestMetricsRouteDeniedCounterIncrements(t *testing.T) {
	gatewayAddr, server, stop := startTestServerWithServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		Metrics:            testMetricsConfig(),
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: "127.0.0.1:1"},
		},
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateLogin))
	readClosed(t, client)
	_ = client.Close()

	waitMetricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "denied"}, 1)
	waitMetricValue(t, server, "mc_gateway_connections_total", map[string]string{"result": "denied", "reason": "route_denied"}, 1)
}

func TestMetricsBackendDialFailureCounterIncrements(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	gatewayAddr, server, stop := startTestServerWithServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		Metrics:            testMetricsConfig(),
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendAddr},
		},
	})
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin))
	readClosed(t, client)
	_ = client.Close()

	waitMetricValue(t, server, "mc_gateway_backend_dials_total", map[string]string{"result": "failed", "reason": "backend_dial_failed"}, 1)
	waitMetricValue(t, server, "mc_gateway_connections_total", map[string]string{"result": "failed", "reason": "backend_dial_failed"}, 1)
}

func TestProxyHandlesMultipleClientsIndependently(t *testing.T) {
	const clients = 5
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	received := make(chan []byte, clients)
	var backendWG sync.WaitGroup
	backendWG.Add(clients)
	for i := 0; i < clients; i++ {
		go func() {
			defer backendWG.Done()
			conn, err := backendListener.Accept()
			if err != nil {
				received <- nil
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			data, _ := io.ReadAll(conn)
			received <- data
		}()
	}

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})
	defer stop()

	want := make(map[string]struct{}, clients)
	var clientWG sync.WaitGroup
	clientErrs := make(chan error, clients)
	clientWG.Add(clients)
	for i := 0; i < clients; i++ {
		payload := []byte{byte(i), byte(i + 1), byte(i + 2)}
		data := append(buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin), payload...)
		want[string(data)] = struct{}{}
		go func(id int, data []byte) {
			defer clientWG.Done()
			if err := sendAndWaitClosed(gatewayAddr, data); err != nil {
				clientErrs <- fmt.Errorf("client %d: %w", id, err)
			}
		}(i, data)
	}
	clientWG.Wait()
	close(clientErrs)
	for err := range clientErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	backendWG.Wait()

	for i := 0; i < clients; i++ {
		got := waitBytes(t, received)
		if _, ok := want[string(got)]; !ok {
			t.Fatalf("unexpected backend bytes: %v", got)
		}
		delete(want, string(got))
	}
	if len(want) != 0 {
		t.Fatalf("missing backend payloads: %d", len(want))
	}
}

func TestProxyShortSoakConcurrentConnections(t *testing.T) {
	const clients = 60
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	received := make(chan []byte, clients)
	backendErrs := make(chan error, clients)
	var backendWG sync.WaitGroup
	backendWG.Add(clients)
	for i := 0; i < clients; i++ {
		go func(id int) {
			defer backendWG.Done()
			conn, err := backendListener.Accept()
			if err != nil {
				backendErrs <- fmt.Errorf("backend accept %d: %w", id, err)
				return
			}
			defer conn.Close()
			if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				backendErrs <- fmt.Errorf("backend deadline %d: %w", id, err)
				return
			}
			data, err := io.ReadAll(conn)
			if err != nil {
				backendErrs <- fmt.Errorf("backend read %d: %w", id, err)
				return
			}
			received <- data
		}(i)
	}

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})
	defer stop()

	want := make(map[string]struct{}, clients)
	var clientWG sync.WaitGroup
	clientErrs := make(chan error, clients)
	clientWG.Add(clients)
	for i := 0; i < clients; i++ {
		payload := []byte{byte(i), byte(i >> 8), 0x42}
		data := append(buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin), payload...)
		want[string(data)] = struct{}{}
		go func(id int, data []byte) {
			defer clientWG.Done()
			if err := sendAndWaitClosed(gatewayAddr, data); err != nil {
				clientErrs <- fmt.Errorf("client %d: %w", id, err)
			}
		}(i, data)
	}

	done := make(chan struct{})
	go func() {
		clientWG.Wait()
		backendWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("soak test timed out waiting for clients and backends")
	}
	close(clientErrs)
	close(backendErrs)
	for err := range clientErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for err := range backendErrs {
		if err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < clients; i++ {
		got := waitBytes(t, received)
		if _, ok := want[string(got)]; !ok {
			t.Fatalf("unexpected backend bytes: %v", got)
		}
		delete(want, string(got))
	}
	if len(want) != 0 {
		t.Fatalf("missing backend payloads: %d", len(want))
	}

	stop()
}

func TestReloadFileUsesNewRoutesForNewConnections(t *testing.T) {
	firstBackend := listenLocalTCP(t)
	defer firstBackend.Close()
	firstBytes := acceptAndReadOnce(t, firstBackend)
	secondBackend := listenLocalTCP(t)
	defer secondBackend.Close()
	secondBytes := acceptAndReadOnce(t, secondBackend)

	configPath := writeRouteConfig(t, firstBackend.Addr().String())
	gatewayAddr, server, stop := startReloadableTestServer(t, configPath)
	defer stop()

	handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
	client := dialAndWrite(t, gatewayAddr, handshake)
	closeClientWrite(t, client)
	_ = client.Close()
	if got := waitBytes(t, firstBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("first backend bytes = %v, want %v", got, handshake)
	}

	writeRouteConfigAt(t, configPath, secondBackend.Addr().String())
	if err := server.ReloadFile(configPath); err != nil {
		t.Fatalf("ReloadFile: %v", err)
	}

	client = dialAndWrite(t, gatewayAddr, handshake)
	closeClientWrite(t, client)
	_ = client.Close()
	if got := waitBytes(t, secondBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("second backend bytes = %v, want %v", got, handshake)
	}
}

func TestReloadFileKeepsCurrentRoutesWhenConfigIsInvalid(t *testing.T) {
	backend := listenLocalTCP(t)
	defer backend.Close()
	firstBytes := acceptAndReadOnce(t, backend)

	configPath := writeRouteConfig(t, backend.Addr().String())
	gatewayAddr, server, stop := startReloadableTestServer(t, configPath)
	defer stop()

	handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
	client := dialAndWrite(t, gatewayAddr, handshake)
	closeClientWrite(t, client)
	_ = client.Close()
	if got := waitBytes(t, firstBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("first backend bytes = %v, want %v", got, handshake)
	}

	if err := os.WriteFile(configPath, []byte("routes:\n  - serverAddress: smp.example.com\n    backend: not-a-host-port\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := server.ReloadFile(configPath); err == nil {
		t.Fatal("ReloadFile succeeded with invalid config")
	}

	secondBytes := acceptAndReadOnce(t, backend)
	client = dialAndWrite(t, gatewayAddr, handshake)
	closeClientWrite(t, client)
	_ = client.Close()
	if got := waitBytes(t, secondBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("backend bytes after failed reload = %v, want %v", got, handshake)
	}
}

func TestReloadFileDoesNotCloseActiveConnections(t *testing.T) {
	const activeClients = 3
	firstBackend := listenLocalTCP(t)
	defer firstBackend.Close()
	secondBackend := listenLocalTCP(t)
	defer secondBackend.Close()
	secondBytes := acceptAndReadOnce(t, secondBackend)

	accepted := make(chan net.Conn, activeClients)
	go func() {
		for i := 0; i < activeClients; i++ {
			conn, err := firstBackend.Accept()
			if err != nil {
				accepted <- nil
				return
			}
			accepted <- conn
		}
	}()

	configPath := writeRouteConfig(t, firstBackend.Addr().String())
	gatewayAddr, server, stop := startReloadableTestServer(t, configPath)
	defer stop()

	handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
	var clients []net.Conn
	var backendConns []net.Conn
	for i := 0; i < activeClients; i++ {
		client := dialAndWrite(t, gatewayAddr, handshake)
		defer client.Close()
		clients = append(clients, client)
		backendConn := waitConn(t, accepted)
		defer backendConn.Close()
		backendConns = append(backendConns, backendConn)
	}

	writeRouteConfigAt(t, configPath, secondBackend.Addr().String())
	if err := server.ReloadFile(configPath); err != nil {
		t.Fatalf("ReloadFile: %v", err)
	}

	for i, client := range clients {
		if err := client.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		_, err := client.Read(make([]byte, 1))
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("active client %d read error = %v, want timeout with connection still open", i, err)
		}
	}

	newClient := dialAndWrite(t, gatewayAddr, handshake)
	closeClientWrite(t, newClient)
	_ = newClient.Close()
	if got := waitBytes(t, secondBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("new connection backend bytes = %v, want %v", got, handshake)
	}

	for _, backendConn := range backendConns {
		_ = backendConn.Close()
	}
	for _, client := range clients {
		readClosed(t, client)
	}
}

func TestReloadMetricsUpdateOnSuccessAndFailure(t *testing.T) {
	firstBackend := listenLocalTCP(t)
	defer firstBackend.Close()
	secondBackend := listenLocalTCP(t)
	defer secondBackend.Close()

	configPath := writeRouteConfigWithMetrics(t, firstBackend.Addr().String())
	_, server, stop := startReloadableTestServer(t, configPath)
	defer stop()
	waitMetricValue(t, server, "mc_gateway_config_generation", nil, 1)
	waitMetricValue(t, server, "mc_gateway_routes", nil, 1)

	writeRoutesConfigAt(t, configPath, []config.Route{
		{ServerAddress: "smp.example.com", Backend: secondBackend.Addr().String()},
		{ServerAddress: "build.example.com", Backend: firstBackend.Addr().String()},
	})
	if err := server.ReloadFile(configPath); err != nil {
		t.Fatalf("ReloadFile valid config: %v", err)
	}
	waitMetricValue(t, server, "mc_gateway_reload_total", map[string]string{"result": "success"}, 1)
	waitMetricValue(t, server, "mc_gateway_config_generation", nil, 2)
	waitMetricValue(t, server, "mc_gateway_routes", nil, 2)

	if err := os.WriteFile(configPath, []byte("routes:\n  - serverAddress: smp.example.com\n    backend: not-a-host-port\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := server.ReloadFile(configPath); err == nil {
		t.Fatal("ReloadFile succeeded with invalid config")
	}
	waitMetricValue(t, server, "mc_gateway_reload_total", map[string]string{"result": "failed"}, 1)
	waitMetricValue(t, server, "mc_gateway_config_generation", nil, 2)
	waitMetricValue(t, server, "mc_gateway_routes", nil, 2)
}

func TestServeStopsOnContextCancellation(t *testing.T) {
	listener := listenLocalTCP(t)
	defer listener.Close()
	cfg := config.Defaults()
	routeTable, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	server := NewServer(cfg, routeTable, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after context cancellation")
	}
}

func startTestServer(t *testing.T, cfg config.Config, configure ...func(*Server)) (string, func()) {
	t.Helper()
	addr, _, stop := startTestServerWithServer(t, cfg, configure...)
	return addr, stop
}

func startTestServerWithServer(t *testing.T, cfg config.Config, configure ...func(*Server)) (string, *Server, func()) {
	t.Helper()
	if cfg.DefaultRoute.Mode == "" && cfg.DefaultRoute.Backend != "" {
		cfg.DefaultRoute.Mode = config.RouteModeAllow
	}
	routeTable, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	listener := listenLocalTCP(t)
	server := NewServer(cfg, routeTable, testLogger())
	for _, fn := range configure {
		fn(server)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			_ = listener.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Serve returned error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("server did not stop")
			}
		})
	}
	return listener.Addr().String(), server, stop
}

func startReloadableTestServer(t *testing.T, configPath string) (string, *Server, func()) {
	t.Helper()
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("config.LoadFile: %v", err)
	}
	routeTable, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	listener := listenLocalTCP(t)
	server := NewServer(cfg, routeTable, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			_ = listener.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Serve returned error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("server did not stop")
			}
		})
	}
	return listener.Addr().String(), server, stop
}

func listenLocalTCP(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	return listener
}

func acceptAndReadOnce(t *testing.T, listener net.Listener) <-chan []byte {
	t.Helper()
	result := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- nil
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		data, _ := io.ReadAll(conn)
		result <- data
	}()
	return result
}

func waitBytes(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case got := <-ch:
		if got == nil {
			t.Fatal("backend did not accept a connection")
		}
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for backend bytes")
		return nil
	}
}

func waitConn(t *testing.T, ch <-chan net.Conn) net.Conn {
	t.Helper()
	select {
	case conn := <-ch:
		if conn == nil {
			t.Fatal("backend did not accept a connection")
		}
		return conn
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for backend connection")
		return nil
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func readClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := waitForConnClosed(conn); err != nil {
		t.Fatal(err)
	}
}

func dialAndWrite(t *testing.T, address string, data []byte) *net.TCPConn {
	t.Helper()
	rawConn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("DialTimeout: %v", err)
	}
	conn := rawConn.(*net.TCPConn)
	if _, err := conn.Write(data); err != nil {
		_ = conn.Close()
		t.Fatalf("Write: %v", err)
	}
	return conn
}

func closeClientWrite(t *testing.T, conn *net.TCPConn) {
	t.Helper()
	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
}

func sendAndWaitClosed(address string, data []byte) error {
	rawConn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn := rawConn.(*net.TCPConn)
	defer conn.Close()
	if err := writeAll(conn, data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := conn.CloseWrite(); err != nil {
		return fmt.Errorf("close write: %w", err)
	}
	if err := waitForConnClosed(conn); err != nil {
		return fmt.Errorf("wait closed: %w", err)
	}
	return nil
}

func waitForConnClosed(conn net.Conn) error {
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}
	_, err := conn.Read(make([]byte, 1))
	if err == nil {
		return errors.New("expected connection to be closed")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("timed out waiting for connection close: %w", err)
	}
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func writeRouteConfig(t *testing.T, backend string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	writeRouteConfigAt(t, path, backend)
	return path
}

func writeRouteConfigWithMetrics(t *testing.T, backend string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	writeRoutesConfigAtWithMetrics(t, path, []config.Route{
		{ServerAddress: "smp.example.com", Backend: backend},
	})
	return path
}

func writeRouteConfigAt(t *testing.T, path string, backend string) {
	t.Helper()
	writeRoutesConfigAt(t, path, []config.Route{
		{ServerAddress: "smp.example.com", Backend: backend},
	})
}

func writeRoutesConfigAt(t *testing.T, path string, routes []config.Route) {
	t.Helper()
	writeRoutesConfig(t, path, routes, false)
}

func writeRoutesConfigAtWithMetrics(t *testing.T, path string, routes []config.Route) {
	t.Helper()
	writeRoutesConfig(t, path, routes, true)
}

func writeRoutesConfig(t *testing.T, path string, routes []config.Route, metricsEnabled bool) {
	t.Helper()
	var routeBody string
	for _, route := range routes {
		routeBody += fmt.Sprintf("  - serverAddress: %s\n    backend: %q\n", route.ServerAddress, route.Backend)
	}
	metricsBlock := ""
	if metricsEnabled {
		metricsBlock = "metrics:\n  enabled: true\n"
	}
	body := fmt.Sprintf(`listen: ":0"
handshakeTimeout: 1s
backendDialTimeout: 1s
%s
unknownHostPolicy: deny
routes:
%s`, metricsBlock, routeBody)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func testMetricsConfig() config.Metrics {
	return config.Metrics{
		Enabled: true,
		Listen:  "127.0.0.1:0",
		Path:    "/metrics",
	}
}

func statusFallbackConfig() config.Config {
	return config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		Fallback: config.Fallback{
			Enabled: true,
			Status: config.FallbackStatus{
				Enabled:         true,
				MOTD:            `Server "unavailable"`,
				ProtocolName:    "mc-gateway",
				ProtocolVersion: 767,
				MaxPlayers:      10,
				OnlinePlayers:   2,
			},
		},
		UnknownHostPolicy: config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: "127.0.0.1:1"},
		},
	}
}

func readStatusResponse(t *testing.T, conn net.Conn) mcproto.StatusResponse {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	packetID, payload, err := mcproto.ReadPacket(conn, mcproto.DefaultLimits().MaxPacketLength)
	if err != nil {
		t.Fatalf("read status response: %v", err)
	}
	if packetID != mcproto.StatusResponsePacketID {
		t.Fatalf("status response packet id = %d, want %d", packetID, mcproto.StatusResponsePacketID)
	}
	statusJSON, remaining := parseStringPayload(t, payload)
	if len(remaining) != 0 {
		t.Fatalf("status response has %d trailing bytes", len(remaining))
	}
	var status mcproto.StatusResponse
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("Unmarshal status response: %v", err)
	}
	return status
}

func parseStringPayload(t *testing.T, payload []byte) (string, []byte) {
	t.Helper()
	reader := bytes.NewReader(payload)
	length, _, err := mcproto.ReadVarInt(reader)
	if err != nil {
		t.Fatalf("read string length: %v", err)
	}
	if length < 0 || length > int32(reader.Len()) {
		t.Fatalf("invalid string length %d with %d bytes remaining", length, reader.Len())
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(reader, raw); err != nil {
		t.Fatalf("read string: %v", err)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read remaining string payload: %v", err)
	}
	return string(raw), remaining
}

func parseLongPayload(t *testing.T, payload []byte) int64 {
	t.Helper()
	if len(payload) != 8 {
		t.Fatalf("long payload length = %d, want 8", len(payload))
	}
	return int64(binary.BigEndian.Uint64(payload))
}

func waitMetricValue(t *testing.T, server *Server, name string, labels map[string]string, want float64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		got, ok := metricValue(t, server, name, labels)
		if ok && got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("metric %s%v = %v (present %v), want %v", name, labels, got, ok, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func metricValue(t *testing.T, server *Server, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	families, err := server.Metrics().Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !metricLabelsMatch(metric, labels) {
				continue
			}
			switch {
			case metric.Gauge != nil:
				return metric.Gauge.GetValue(), true
			case metric.Counter != nil:
				return metric.Counter.GetValue(), true
			case metric.Histogram != nil:
				return float64(metric.Histogram.GetSampleCount()), true
			}
		}
	}
	return 0, false
}

func metricLabelsMatch(metric *dto.Metric, labels map[string]string) bool {
	if len(metric.GetLabel()) != len(labels) {
		return false
	}
	for _, label := range metric.GetLabel() {
		if labels[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}

func buildHandshakePacket(protocol int32, address string, port uint16, nextState int32) []byte {
	var payload []byte
	payload = append(payload, mcproto.WriteVarInt(mcproto.HandshakePacketID)...)
	payload = append(payload, mcproto.WriteVarInt(protocol)...)
	payload = append(payload, mcproto.WriteVarInt(int32(len(address)))...)
	payload = append(payload, []byte(address)...)
	var portRaw [2]byte
	binary.BigEndian.PutUint16(portRaw[:], port)
	payload = append(payload, portRaw[:]...)
	payload = append(payload, mcproto.WriteVarInt(nextState)...)
	return append(mcproto.WriteVarInt(int32(len(payload))), payload...)
}
