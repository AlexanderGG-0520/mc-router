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
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
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

func TestProxyRoutesTransferHandshakeToKnownBackend(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendBytes := acceptAndReadOnce(t, backendListener)

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "transfer.example.com", Backend: backendListener.Addr().String(), StatusBackend: "127.0.0.1:1"},
		},
	})
	defer stop()

	handshake := buildHandshakePacket(765, "TRANSFER.Example.COM.", 25565, mcproto.NextStateTransfer)
	remaining := []byte{0x05, 0x06, 0x07}
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

func TestProxyDeniesClientByCIDRBeforeHandshake(t *testing.T) {
	dialed := make(chan struct{}, 1)

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		ClientPolicy:       config.ClientPolicy{Deny: []string{"127.0.0.1/32"}},
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

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	readClosed(t, client)

	select {
	case <-dialed:
		t.Fatal("dialer was called for a denied client")
	default:
	}
}

func TestProxyClientAllowListTakesPrecedenceOverDenyList(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendBytes := acceptAndReadOnce(t, backendListener)

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		ClientPolicy: config.ClientPolicy{
			Allow: []string{"127.0.0.1"},
			Deny:  []string{"127.0.0.1"},
		},
		UnknownHostPolicy: config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()},
		},
	})
	defer stop()

	handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
	client := dialAndWrite(t, gatewayAddr, handshake)
	defer client.Close()
	closeClientWrite(t, client)
	if got := waitBytes(t, backendBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("backend bytes = %v, want %v", got, handshake)
	}
}

func TestProxyRateLimitsRepeatedConnectionsFromOneClient(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendBytes := acceptAndReadOnce(t, backendListener)

	cfg := validProxyConfig()
	cfg.ClientRateLimit = config.ClientRateLimit{
		Enabled:              true,
		ConnectionsPerSecond: 1,
		Burst:                1,
		IdleTimeout:          config.Duration{Duration: time.Minute},
		MaxEntries:           2,
	}
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()}}
	gatewayAddr, stop := startTestServer(t, cfg)
	defer stop()

	handshake := buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin)
	first := dialAndWrite(t, gatewayAddr, handshake)
	defer first.Close()
	closeClientWrite(t, first)
	if got := waitBytes(t, backendBytes); !bytes.Equal(got, handshake) {
		t.Fatalf("first backend bytes = %v, want %v", got, handshake)
	}

	second := dialAndWrite(t, gatewayAddr, handshake)
	defer second.Close()
	readClosed(t, second)
}

func TestProxyDeniesUnknownTransferHostWithoutConnectingBackend(t *testing.T) {
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

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateTransfer))
	defer client.Close()
	closeClientWrite(t, client)
	readClosed(t, client)

	select {
	case <-dialed:
		t.Fatal("dialer was called for an unknown transfer host")
	default:
	}
}

func TestRouteStatusOverrideRespondsWithoutDialingBackend(t *testing.T) {
	dialed := make(chan struct{}, 1)
	cfg := validProxyConfig()
	cfg.Routes = []config.Route{
		{
			ServerAddress: "smp.example.com",
			Backend:       "127.0.0.1:1",
			StatusBackend: "127.0.0.1:1",
			StatusOverride: &config.StatusOverride{
				MOTD:            "Alec SMP",
				ProtocolName:    "Alec SMP 2",
				ProtocolVersion: 767,
				MaxPlayers:      100,
				OnlinePlayers:   12,
			},
		},
	}
	gatewayAddr, _, stop := startTestServerWithServer(t, cfg, func(server *Server) {
		server.dialContext = func(context.Context, string, string) (net.Conn, error) {
			dialed <- struct{}{}
			return nil, errors.New("dial should not be called")
		}
	})
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer conn.Close()

	status := readStatusResponse(t, conn)
	if status.Version.Name != "Alec SMP 2" || status.Version.Protocol != 767 {
		t.Fatalf("status version = %#v", status.Version)
	}
	if status.Players.Max != 100 || status.Players.Online != 12 {
		t.Fatalf("status players = %#v", status.Players)
	}
	if status.Description.Text != "Alec SMP" {
		t.Fatalf("status motd = %q", status.Description.Text)
	}

	const pingPayload int64 = 0x1122334455667788
	if err := writeAll(conn, mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(pingPayload))); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if got := readStatusPong(t, conn); got != pingPayload {
		t.Fatalf("pong payload = %x, want %x", got, pingPayload)
	}

	select {
	case <-dialed:
		t.Fatal("dialer was called for a route status override")
	default:
	}
}

func TestStatusFallbackDisabledDeniesUnknownHostWithClose(t *testing.T) {
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

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "route_denied"})
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
	waitMetricValue(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "route_denied"}, 1)
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
	cfg := statusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "route_denied"})
}

func TestLoginFallbackDisabledDeniesUnknownHostWithClose(t *testing.T) {
	cfg := loginFallbackConfig()
	cfg.Fallback.Login.Enabled = false
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(mcproto.ProtocolMinecraft1211, "unknown.example.com", 25565, mcproto.NextStateLogin),
		buildLoginStartPacket("FallbackUser")...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "login", "reason": "route_denied"})
}

func TestLoginFallbackRespondsForRouteDeniedLoginStart(t *testing.T) {
	cfg := loginFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(mcproto.ProtocolMinecraft1211, "unknown.example.com", 25565, mcproto.NextStateLogin),
		buildLoginStartPacket("FallbackUser")...,
	))
	defer conn.Close()

	reasonJSON, reason := readLoginDisconnectReason(t, conn)
	if reasonJSON != `{"text":"Server \"unavailable\""}` {
		t.Fatalf("login disconnect reason JSON = %q", reasonJSON)
	}
	if reason.Text != `Server "unavailable"` {
		t.Fatalf("login disconnect reason = %q", reason.Text)
	}
	waitMetricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "denied"}, 1)
	waitMetricValue(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "login", "reason": "route_denied"}, 1)
}

func TestLoginFallbackRouteDeniedResponseCanBeDisabled(t *testing.T) {
	cfg := loginFallbackConfig()
	cfg.Fallback.Login.RespondOnRouteDenied = boolPtr(false)
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(mcproto.ProtocolMinecraft1211, "unknown.example.com", 25565, mcproto.NextStateLogin),
		buildLoginStartPacket("FallbackUser")...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "login", "reason": "route_denied"})
}

func TestLoginFallbackClosesMalformedLoginStart(t *testing.T) {
	cfg := loginFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(mcproto.ProtocolMinecraft1211, "unknown.example.com", 25565, mcproto.NextStateLogin),
		mcproto.BuildPacket(0x01)...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "login", "reason": "route_denied"})
}

func TestLoginFallbackClosesUnsupportedProtocol(t *testing.T) {
	cfg := loginFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateLogin),
		buildLoginStartPacket("FallbackUser")...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "login", "reason": "route_denied"})
}

func TestLoginFallbackDoesNotOverrideKnownRoute(t *testing.T) {
	backendListener := listenLocalTCP(t)
	defer backendListener.Close()
	backendBytes := acceptAndReadOnce(t, backendListener)

	cfg := loginFallbackConfig()
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: backendListener.Addr().String()}}
	gatewayAddr, stop := startTestServer(t, cfg)
	defer stop()

	handshake := buildHandshakePacket(mcproto.ProtocolMinecraft1211, "smp.example.com", 25565, mcproto.NextStateLogin)
	loginStart := buildLoginStartPacket("FallbackUser")
	client := dialAndWrite(t, gatewayAddr, append(append([]byte{}, handshake...), loginStart...))
	defer client.Close()
	closeClientWrite(t, client)

	if got := waitBytes(t, backendBytes); !bytes.Equal(got, append(append([]byte{}, handshake...), loginStart...)) {
		t.Fatalf("backend bytes = %v, want handshake plus login start", got)
	}
}

func TestLoginFallbackDoesNotOverrideDefaultRoute(t *testing.T) {
	defaultBackend := listenLocalTCP(t)
	defer defaultBackend.Close()
	backendBytes := acceptAndReadOnce(t, defaultBackend)

	cfg := loginFallbackConfig()
	cfg.UnknownHostPolicy = config.UnknownHostDefault
	cfg.DefaultRoute = config.DefaultRoute{Backend: defaultBackend.Addr().String(), Mode: config.RouteModeAllow}
	cfg.Routes = nil
	gatewayAddr, stop := startTestServer(t, cfg)
	defer stop()

	handshake := buildHandshakePacket(mcproto.ProtocolMinecraft1211, "unknown.example.com", 25565, mcproto.NextStateLogin)
	loginStart := buildLoginStartPacket("FallbackUser")
	client := dialAndWrite(t, gatewayAddr, append(append([]byte{}, handshake...), loginStart...))
	defer client.Close()
	closeClientWrite(t, client)

	if got := waitBytes(t, backendBytes); !bytes.Equal(got, append(append([]byte{}, handshake...), loginStart...)) {
		t.Fatalf("default backend bytes = %v, want handshake plus login start", got)
	}
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
	cfg := statusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(1))...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "route_denied"})
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

func TestStatusFallbackRespondsForBackendDialFailure(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	cfg := backendFailureStatusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: backendAddr}}
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer conn.Close()

	status := readStatusResponse(t, conn)
	if status.Description.Text != `Server "unavailable"` {
		t.Fatalf("status motd = %q", status.Description.Text)
	}

	const pingPayload int64 = 0x0102030405060708
	if err := writeAll(conn, mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(pingPayload))); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if got := readStatusPong(t, conn); got != pingPayload {
		t.Fatalf("pong payload = %x, want %x", got, pingPayload)
	}

	waitMetricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "matched"}, 1)
	if got, ok := metricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "denied"}); ok && got != 0 {
		t.Fatalf("route denied metric = %v, want absent or 0", got)
	}
	waitMetricValue(t, server, "mc_gateway_backend_dials_total", map[string]string{"result": "failed", "reason": "backend_dial_failed"}, 1)
	waitMetricValue(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "backend_dial_failed"}, 1)
	waitMetricValue(t, server, "mc_gateway_connections_total", map[string]string{"result": "failed", "reason": "backend_dial_failed"}, 1)
}

func TestStatusFallbackRespondsForDefaultBackendDialFailure(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	cfg := backendFailureStatusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	cfg.UnknownHostPolicy = config.UnknownHostDefault
	cfg.DefaultRoute = config.DefaultRoute{Backend: backendAddr, Mode: config.RouteModeAllow}
	cfg.Routes = nil
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "unknown.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer conn.Close()
	_ = readStatusResponse(t, conn)

	waitMetricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "default"}, 1)
	if got, ok := metricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "denied"}); ok && got != 0 {
		t.Fatalf("route denied metric = %v, want absent or 0", got)
	}
	waitMetricValue(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "backend_dial_failed"}, 1)
}

func TestStatusFallbackRespondsForBackendDialTimeout(t *testing.T) {
	dialStarted := make(chan struct{})
	cfg := backendFailureStatusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	cfg.BackendDialTimeout = config.Duration{Duration: 40 * time.Millisecond}
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg, func(server *Server) {
		server.dialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
			close(dialStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer conn.Close()
	waitClosed(t, dialStarted, "dialer was not called")

	status := readStatusResponse(t, conn)
	if status.Version.Protocol != 767 {
		t.Fatalf("status protocol = %d", status.Version.Protocol)
	}
	waitMetricValue(t, server, "mc_gateway_route_decisions_total", map[string]string{"result": "matched"}, 1)
	waitMetricValue(t, server, "mc_gateway_backend_dials_total", map[string]string{"result": "failed", "reason": "backend_dial_timeout"}, 1)
	waitMetricValue(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "backend_dial_timeout"}, 1)
}

func TestStatusFallbackBackendFailureDisabledClosesStatusClient(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	cfg := statusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: backendAddr}}
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "backend_dial_failed"})
}

func TestStatusFallbackDoesNotHandleLoginBackendDialFailure(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	cfg := backendFailureStatusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: backendAddr}}
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateLogin))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "backend_dial_failed"})
}

func TestLoginFallbackDoesNotHandleBackendDialFailure(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	cfg := loginFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: backendAddr}}
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(mcproto.ProtocolMinecraft1211, "smp.example.com", 25565, mcproto.NextStateLogin),
		buildLoginStartPacket("FallbackUser")...,
	))
	defer client.Close()
	readClosed(t, client)
	waitMetricValue(t, server, "mc_gateway_backend_dials_total", map[string]string{"result": "failed", "reason": "backend_dial_failed"}, 1)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "login", "reason": "backend_dial_failed"})
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "login", "reason": "route_denied"})
}

func TestLoginFallbackMetricsDisabledDoesNotPanic(t *testing.T) {
	cfg := loginFallbackConfig()
	gatewayAddr, stop := startTestServer(t, cfg)
	defer stop()

	conn := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(mcproto.ProtocolMinecraft1211, "unknown.example.com", 25565, mcproto.NextStateLogin),
		buildLoginStartPacket("FallbackUser")...,
	))
	defer conn.Close()
	_, reason := readLoginDisconnectReason(t, conn)
	if reason.Text != `Server "unavailable"` {
		t.Fatalf("login disconnect reason = %q", reason.Text)
	}
}

func TestStatusFallbackBackendFailureClosesMalformedStatusRequest(t *testing.T) {
	backendListener := listenLocalTCP(t)
	backendAddr := backendListener.Addr().String()
	if err := backendListener.Close(); err != nil {
		t.Fatalf("backend listener close: %v", err)
	}

	cfg := backendFailureStatusFallbackConfig()
	cfg.Metrics = testMetricsConfig()
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: backendAddr}}
	gatewayAddr, server, stop := startTestServerWithServer(t, cfg)
	defer stop()

	client := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(1))...,
	))
	defer client.Close()
	readClosed(t, client)
	assertMetricAbsentOrZero(t, server, "mc_gateway_fallback_responses_total", map[string]string{"state": "status", "reason": "backend_dial_failed"})
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

func TestReloadFileUsesNewStatusBackendForNewStatusConnections(t *testing.T) {
	firstBackend := startStatusProtocolBackend(t)
	defer firstBackend.close()
	secondBackend := startStatusProtocolBackend(t)
	defer secondBackend.close()

	configPath := t.TempDir() + "/config.yaml"
	writeRoutesConfigAt(t, configPath, []config.Route{{
		ServerAddress: "smp.example.com",
		Backend:       "127.0.0.1:1",
		StatusBackend: firstBackend.addr,
	}})
	gatewayAddr, server, stop := startReloadableTestServer(t, configPath)
	defer stop()

	firstClient := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer firstClient.Close()
	_ = readStatusResponse(t, firstClient)

	writeRoutesConfigAt(t, configPath, []config.Route{{
		ServerAddress: "smp.example.com",
		Backend:       "127.0.0.1:1",
		StatusBackend: secondBackend.addr,
	}})
	if err := server.ReloadFile(configPath); err != nil {
		t.Fatalf("ReloadFile: %v", err)
	}

	if err := writeAll(firstClient, mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(1))); err != nil {
		t.Fatalf("write first ping: %v", err)
	}
	if got := readStatusPong(t, firstClient); got != 1 {
		t.Fatalf("first pong payload = %d, want 1", got)
	}
	_ = waitProtocolResult(t, firstBackend.result)

	secondClient := dialAndWrite(t, gatewayAddr, append(
		buildHandshakePacket(765, "smp.example.com", 25565, mcproto.NextStateStatus),
		mcproto.BuildPacket(mcproto.StatusRequestPacketID)...,
	))
	defer secondClient.Close()
	_ = readStatusResponse(t, secondClient)
	if err := writeAll(secondClient, mcproto.BuildPacket(mcproto.StatusPingPacketID, mcproto.EncodeLong(2))); err != nil {
		t.Fatalf("write second ping: %v", err)
	}
	if got := readStatusPong(t, secondClient); got != 2 {
		t.Fatalf("second pong payload = %d, want 2", got)
	}
	_ = waitProtocolResult(t, secondBackend.result)
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

func TestBuildRouteSnapshotWithEmptyDiscoveredRoutesMatchesStaticRouter(t *testing.T) {
	cfg := validProxyConfig()
	cfg.Routes = []config.Route{
		{ServerAddress: "SMP.Example.COM.", Backend: "static.example.com:25565"},
	}

	snapshot, err := BuildRouteSnapshot(cfg, nil)
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	selection, err := snapshot.Router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selection.Backend != "static.example.com:25565" {
		t.Fatalf("backend = %q, want static.example.com:25565", selection.Backend)
	}
	if snapshot.DiscoveryMerge.Stats.DiscoveredRoutes != 0 {
		t.Fatalf("discovered routes = %d, want 0", snapshot.DiscoveryMerge.Stats.DiscoveredRoutes)
	}
	if len(snapshot.DiscoveryMerge.Ignored) != 0 {
		t.Fatalf("ignored routes = %d, want 0", len(snapshot.DiscoveryMerge.Ignored))
	}
}

func TestBuildRouteSnapshotIncludesInMemoryDiscoveredRoute(t *testing.T) {
	cfg := validProxyConfig()

	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "SMP.Example.COM.", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	selection, err := snapshot.Router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selection.Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("backend = %q, want smp.minecraft.svc.cluster.local:25565", selection.Backend)
	}
	if snapshot.DiscoveryMerge.Stats.MergedRoutes != 1 {
		t.Fatalf("merged routes = %d, want 1", snapshot.DiscoveryMerge.Stats.MergedRoutes)
	}
}

func TestBuildRouteSnapshotKeepsStaticRouteWhenDiscoveredConflicts(t *testing.T) {
	cfg := validProxyConfig()
	cfg.Routes = []config.Route{
		{ServerAddress: "SMP.Example.COM.", Backend: "static.example.com:25565"},
	}

	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	selection, err := snapshot.Router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selection.Backend != "static.example.com:25565" {
		t.Fatalf("backend = %q, want static.example.com:25565", selection.Backend)
	}
	if snapshot.DiscoveryMerge.Stats.IgnoredByReason[discovery.ReasonStaticRoutePrecedence] != 1 {
		t.Fatalf("static precedence ignored count = %d, want 1", snapshot.DiscoveryMerge.Stats.IgnoredByReason[discovery.ReasonStaticRoutePrecedence])
	}
}

func TestBuildRouteSnapshotDoesNotInsertDefaultRouteIntoExplicitRoutes(t *testing.T) {
	cfg := validProxyConfig()
	cfg.UnknownHostPolicy = config.UnknownHostDefault
	cfg.DefaultRoute = config.DefaultRoute{Backend: "default.example.com:25565", Mode: config.RouteModeAllow}

	snapshot, err := BuildRouteSnapshot(cfg, nil)
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	if len(snapshot.Config.Routes) != 0 {
		t.Fatalf("explicit routes = %d, want 0", len(snapshot.Config.Routes))
	}
	selection, err := snapshot.Router.Select("unknown.example.com")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selection.Backend != "default.example.com:25565" {
		t.Fatalf("default backend = %q, want default.example.com:25565", selection.Backend)
	}
}

func TestBuildRouteSnapshotValidatesStaticConfigBeforeMerge(t *testing.T) {
	cfg := validProxyConfig()
	cfg.Routes = []config.Route{
		{ServerAddress: "bad host.example.com", Backend: "static.example.com:25565"},
	}

	_, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err == nil {
		t.Fatal("BuildRouteSnapshot succeeded with invalid static config")
	}
}

func TestReloadFileBuildsStaticOnlyRouteSnapshot(t *testing.T) {
	firstBackend := listenLocalTCP(t)
	defer firstBackend.Close()
	secondBackend := listenLocalTCP(t)
	defer secondBackend.Close()

	configPath := writeRouteConfig(t, firstBackend.Addr().String())
	_, server, stop := startReloadableTestServer(t, configPath)
	defer stop()

	writeRoutesConfigAt(t, configPath, []config.Route{
		{ServerAddress: "smp.example.com", Backend: secondBackend.Addr().String()},
		{ServerAddress: "build.example.com", Backend: firstBackend.Addr().String()},
	})
	if err := server.ReloadFile(configPath); err != nil {
		t.Fatalf("ReloadFile: %v", err)
	}
	state := server.currentState()
	if state.discoveryMerge.Stats.DiscoveredRoutes != 0 {
		t.Fatalf("reload discovered routes = %d, want 0", state.discoveryMerge.Stats.DiscoveredRoutes)
	}
	if state.discoveryMerge.Stats.MergedRoutes != 2 {
		t.Fatalf("reload merged routes = %d, want 2", state.discoveryMerge.Stats.MergedRoutes)
	}
}

func TestReloadFilePreservesStartupDiscoveredRoutes(t *testing.T) {
	staticBackend := listenLocalTCP(t)
	defer staticBackend.Close()

	configPath := writeRouteConfig(t, staticBackend.Addr().String())
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("config.LoadFile: %v", err)
	}
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "discovered.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	secondBackend := listenLocalTCP(t)
	defer secondBackend.Close()
	writeRoutesConfigAt(t, configPath, []config.Route{
		{ServerAddress: "smp.example.com", Backend: secondBackend.Addr().String()},
	})
	if err := server.ReloadFile(configPath); err != nil {
		t.Fatalf("ReloadFile: %v", err)
	}

	state := server.currentState()
	if state.discoveryMerge.Stats.DiscoveredRoutes != 1 {
		t.Fatalf("reload discovered routes = %d, want 1", state.discoveryMerge.Stats.DiscoveredRoutes)
	}
	selection, err := state.router.Select("discovered.example.com")
	if err != nil {
		t.Fatalf("Select discovered route after reload: %v", err)
	}
	if selection.Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("discovered backend = %q, want smp.minecraft.svc.cluster.local:25565", selection.Backend)
	}
}

func TestUpdateDiscoveredRoutesAddsUpdatesAndDeletesRoutes(t *testing.T) {
	cfg := validProxyConfig()
	snapshot, err := BuildRouteSnapshot(cfg, nil)
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	if err := server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})); err != nil {
		t.Fatalf("UpdateDiscoveredRoutes add: %v", err)
	}
	selection, err := server.currentState().router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select added discovered route: %v", err)
	}
	if selection.Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("added backend = %q", selection.Backend)
	}

	if err := server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp-new.minecraft.svc.cluster.local:25566"},
	})); err != nil {
		t.Fatalf("UpdateDiscoveredRoutes update: %v", err)
	}
	selection, err = server.currentState().router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select updated discovered route: %v", err)
	}
	if selection.Backend != "smp-new.minecraft.svc.cluster.local:25566" {
		t.Fatalf("updated backend = %q", selection.Backend)
	}

	if err := server.UpdateDiscoveredRoutes(context.Background(), nil); err != nil {
		t.Fatalf("UpdateDiscoveredRoutes delete: %v", err)
	}
	if _, err := server.currentState().router.Select("smp.example.com"); err == nil {
		t.Fatal("deleted discovered route is still selectable")
	}
}

func TestUpdateDiscoveredRoutesKeepsStaticPrecedenceAndDefaultRoute(t *testing.T) {
	cfg := validProxyConfig()
	cfg.UnknownHostPolicy = config.UnknownHostDefault
	cfg.DefaultRoute = config.DefaultRoute{Backend: "default.example.com:25565", Mode: config.RouteModeAllow}
	cfg.Routes = []config.Route{{ServerAddress: "smp.example.com", Backend: "static.example.com:25565"}}
	snapshot, err := BuildRouteSnapshot(cfg, nil)
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	if err := server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
		{Host: "build.example.com", Backend: "build.minecraft.svc.cluster.local:25565"},
	})); err != nil {
		t.Fatalf("UpdateDiscoveredRoutes: %v", err)
	}

	state := server.currentState()
	selection, err := state.router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select static route: %v", err)
	}
	if selection.Backend != "static.example.com:25565" {
		t.Fatalf("static backend = %q", selection.Backend)
	}
	if len(state.cfg.Routes) != 2 {
		t.Fatalf("explicit routes = %d, want static plus one discovered route", len(state.cfg.Routes))
	}
	selection, err = state.router.Select("unknown.example.com")
	if err != nil {
		t.Fatalf("Select default route: %v", err)
	}
	if selection.Backend != "default.example.com:25565" {
		t.Fatalf("default backend = %q", selection.Backend)
	}
}

func TestUpdateDiscoveredRoutesFailureKeepsExistingSnapshot(t *testing.T) {
	cfg := validProxyConfig()
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	snapshot.StaticConfig.Routes = []config.Route{
		{ServerAddress: "duplicate.example.com", Backend: "first.example.com:25565"},
		{ServerAddress: "duplicate.example.com", Backend: "second.example.com:25565"},
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	err = server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "bad.example.com", Backend: "bad.minecraft.svc.cluster.local:25565"},
	}))
	if err == nil {
		t.Fatal("UpdateDiscoveredRoutes succeeded with invalid static config")
	}
	selection, err := server.currentState().router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("existing route was not preserved: %v", err)
	}
	if selection.Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("preserved backend = %q", selection.Backend)
	}
	if _, err := server.currentState().router.Select("bad.example.com"); err == nil {
		t.Fatal("invalid discovered route was swapped into the active snapshot")
	}
}

func TestUpdateDiscoveredRoutesProviderErrorKeepsExistingSnapshot(t *testing.T) {
	cfg := validProxyConfig()
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())
	providerErr := errors.New("provider failed")

	err = server.UpdateDiscoveredRoutes(context.Background(), &errorProvider{err: providerErr})
	if !errors.Is(err, providerErr) {
		t.Fatalf("UpdateDiscoveredRoutes error = %v, want %v", err, providerErr)
	}
	selection, err := server.currentState().router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("existing route was not preserved: %v", err)
	}
	if selection.Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("preserved backend = %q", selection.Backend)
	}
}

func TestKubernetesDiscoveryMetricsTrackStartupAndRuntimeSuccess(t *testing.T) {
	cfg := validProxyConfig()
	cfg.Metrics.Enabled = true
	cfg.Discovery.Kubernetes.Enabled = true
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "startup.example.com", Backend: "startup.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	waitMetricValue(t, server, "mc_gateway_kubernetes_discovered_routes", nil, 1)
	if got, ok := metricValue(t, server, "mc_gateway_kubernetes_last_successful_sync_timestamp_seconds", nil); !ok || got <= 0 {
		t.Fatalf("last successful sync timestamp = %v (present %v), want > 0", got, ok)
	}

	if err := server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
		{Host: "build.example.com", Backend: "build.minecraft.svc.cluster.local:25565"},
	})); err != nil {
		t.Fatalf("UpdateDiscoveredRoutes: %v", err)
	}
	waitMetricValue(t, server, "mc_gateway_kubernetes_discovered_routes", nil, 2)
}

func TestKubernetesDiscoveryMetricsTrackRebuildFailureAndKeepRouteGauge(t *testing.T) {
	cfg := validProxyConfig()
	cfg.Metrics.Enabled = true
	cfg.Discovery.Kubernetes.Enabled = true
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	snapshot.StaticConfig.Routes = []config.Route{
		{ServerAddress: "duplicate.example.com", Backend: "first.example.com:25565"},
		{ServerAddress: "duplicate.example.com", Backend: "second.example.com:25565"},
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	waitMetricValue(t, server, "mc_gateway_kubernetes_discovered_routes", nil, 1)
	err = server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "bad.example.com", Backend: "bad.minecraft.svc.cluster.local:25565"},
	}))
	if err == nil {
		t.Fatal("UpdateDiscoveredRoutes succeeded with invalid static config")
	}
	waitMetricValue(t, server, "mc_gateway_kubernetes_discovery_errors_total", map[string]string{"reason": "rebuild_failed"}, 1)
	waitMetricValue(t, server, "mc_gateway_kubernetes_discovered_routes", nil, 1)
}

func TestKubernetesDiscoveryMetricsCanceledRuntimeUpdateIsNotError(t *testing.T) {
	cfg := validProxyConfig()
	cfg.Metrics.Enabled = true
	cfg.Discovery.Kubernetes.Enabled = true
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.UpdateDiscoveredRoutes(ctx, discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "new.example.com", Backend: "new.minecraft.svc.cluster.local:25565"},
	})); !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateDiscoveredRoutes error = %v, want context.Canceled", err)
	}
	assertMetricAbsentOrZero(t, server, "mc_gateway_kubernetes_discovery_errors_total", map[string]string{"reason": "rebuild_failed"})
	waitMetricValue(t, server, "mc_gateway_kubernetes_discovered_routes", nil, 1)
}

func TestUpdateDiscoveredRoutesCanceledContextKeepsExistingSnapshot(t *testing.T) {
	cfg := validProxyConfig()
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.UpdateDiscoveredRoutes(ctx, discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "new.example.com", Backend: "new.minecraft.svc.cluster.local:25565"},
	})); !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateDiscoveredRoutes error = %v, want context.Canceled", err)
	}
	if _, err := server.currentState().router.Select("smp.example.com"); err != nil {
		t.Fatalf("existing route was not preserved: %v", err)
	}
	if _, err := server.currentState().router.Select("new.example.com"); err == nil {
		t.Fatal("canceled update changed the active snapshot")
	}
}

func TestReloadAndWatchUpdatesShareLatestInputs(t *testing.T) {
	staticBackend := listenLocalTCP(t)
	defer staticBackend.Close()
	reloadedBackend := listenLocalTCP(t)
	defer reloadedBackend.Close()

	configPath := writeRouteConfig(t, staticBackend.Addr().String())
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("config.LoadFile: %v", err)
	}
	snapshot, err := BuildRouteSnapshot(cfg, nil)
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	if err := server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "discovered.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})); err != nil {
		t.Fatalf("UpdateDiscoveredRoutes initial: %v", err)
	}

	writeRoutesConfigAt(t, configPath, []config.Route{
		{ServerAddress: "smp.example.com", Backend: reloadedBackend.Addr().String()},
	})
	if err := server.ReloadFile(configPath); err != nil {
		t.Fatalf("ReloadFile: %v", err)
	}
	if _, err := server.currentState().router.Select("discovered.example.com"); err != nil {
		t.Fatalf("reload dropped latest discovered route: %v", err)
	}

	if err := server.UpdateDiscoveredRoutes(context.Background(), discovery.NewMemoryProvider([]kubernetes.DiscoveredRoute{
		{Host: "new.example.com", Backend: "new.minecraft.svc.cluster.local:25565"},
	})); err != nil {
		t.Fatalf("UpdateDiscoveredRoutes after reload: %v", err)
	}
	selection, err := server.currentState().router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("watch update dropped latest static route: %v", err)
	}
	if selection.Backend != reloadedBackend.Addr().String() {
		t.Fatalf("static backend after watch update = %q, want %q", selection.Backend, reloadedBackend.Addr().String())
	}
	if _, err := server.currentState().router.Select("discovered.example.com"); err == nil {
		t.Fatal("old discovered route survived complete replacement update")
	}
}

func TestSnapshotReturnsCopies(t *testing.T) {
	cfg := validProxyConfig()
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	server := NewServerFromSnapshot(snapshot, testLogger())

	got := server.Snapshot()
	got.StaticConfig.Routes = append(got.StaticConfig.Routes, config.Route{ServerAddress: "mutated.example.com", Backend: "mutated.example.com:25565"})
	got.Config.Routes[0].Backend = "mutated.example.com:25565"
	got.DiscoveredRoutes[0].Backend = "mutated.minecraft.svc.cluster.local:25565"

	again := server.Snapshot()
	if len(again.StaticConfig.Routes) != len(cfg.Routes) {
		t.Fatalf("static routes were mutated through snapshot copy")
	}
	if again.DiscoveredRoutes[0].Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("discovered backend was mutated through snapshot copy: %q", again.DiscoveredRoutes[0].Backend)
	}
}

func TestNewServerFromSnapshotCopiesMutableSnapshotData(t *testing.T) {
	cfg := validProxyConfig()
	cfg.Routes = []config.Route{
		{ServerAddress: "smp.example.com", Backend: "static.example.com:25565"},
	}
	snapshot, err := BuildRouteSnapshot(cfg, []kubernetes.DiscoveredRoute{
		{Host: "SMP.Example.COM.", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}

	server := NewServerFromSnapshot(snapshot, testLogger())
	snapshot.Config.Routes[0].Backend = "mutated.example.com:25565"
	snapshot.DiscoveredRoutes[0].Backend = "mutated.minecraft.svc.cluster.local:25565"
	snapshot.DiscoveryMerge.Stats.IgnoredByReason[discovery.ReasonStaticRoutePrecedence] = 99

	state := server.currentState()
	if state.cfg.Routes[0].Backend != "static.example.com:25565" {
		t.Fatalf("stored backend = %q, want static.example.com:25565", state.cfg.Routes[0].Backend)
	}
	if state.discoveryMerge.Stats.IgnoredByReason[discovery.ReasonStaticRoutePrecedence] != 1 {
		t.Fatalf("stored ignored count = %d, want 1", state.discoveryMerge.Stats.IgnoredByReason[discovery.ReasonStaticRoutePrecedence])
	}
	if state.discoveredRoutes[0].Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("stored discovered backend = %q, want smp.minecraft.svc.cluster.local:25565", state.discoveredRoutes[0].Backend)
	}
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

func validProxyConfig() config.Config {
	cfg := config.Defaults()
	cfg.Listen = ":0"
	return cfg
}

func startTestServerWithServer(t *testing.T, cfg config.Config, configure ...func(*Server)) (string, *Server, func()) {
	t.Helper()
	if cfg.DefaultRoute.Mode == "" && cfg.DefaultRoute.Backend != "" {
		cfg.DefaultRoute.Mode = config.RouteModeAllow
	}
	snapshot, err := BuildRouteSnapshot(cfg, nil)
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	listener := listenLocalTCP(t)
	server := NewServerFromSnapshot(snapshot, testLogger())
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
	snapshot, err := BuildRouteSnapshot(cfg, nil)
	if err != nil {
		t.Fatalf("BuildRouteSnapshot: %v", err)
	}
	listener := listenLocalTCP(t)
	server := NewServerFromSnapshot(snapshot, testLogger())
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

const (
	testBackendReadTimeout = 3 * time.Second
	testWaitTimeout        = 5 * time.Second
)

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

		if err := conn.SetReadDeadline(time.Now().Add(testBackendReadTimeout)); err != nil {
			result <- nil
			return
		}

		data, err := io.ReadAll(conn)
		if err != nil {
			result <- nil
			return
		}
		result <- data
	}()

	return result
}

func waitBytes(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()

	select {
	case got := <-ch:
		if got == nil {
			t.Fatal("backend did not accept or read the connection")
		}
		return got
	case <-time.After(testWaitTimeout):
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
		if route.StatusBackend != "" {
			routeBody += fmt.Sprintf("    statusBackend: %q\n", route.StatusBackend)
		}
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

func backendFailureStatusFallbackConfig() config.Config {
	cfg := statusFallbackConfig()
	cfg.Fallback.Status.RespondOnBackendFailure = true
	return cfg
}

func loginFallbackConfig() config.Config {
	return config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		Fallback: config.Fallback{
			Enabled: true,
			Login: config.FallbackLogin{
				Enabled:              true,
				RespondOnRouteDenied: boolPtr(true),
				Message:              `Server "unavailable"`,
			},
		},
		UnknownHostPolicy: config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "smp.example.com", Backend: "127.0.0.1:1"},
		},
	}
}

func boolPtr(value bool) *bool {
	return &value
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

func readStatusPong(t *testing.T, conn net.Conn) int64 {
	t.Helper()
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
	return parseLongPayload(t, payload)
}

func readLoginDisconnectReason(t *testing.T, conn net.Conn) (string, mcproto.StatusChatComponent) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	packetID, payload, err := mcproto.ReadPacket(conn, mcproto.DefaultLimits().MaxPacketLength)
	if err != nil {
		t.Fatalf("read login disconnect: %v", err)
	}
	if packetID != mcproto.LoginDisconnectPacketID {
		t.Fatalf("login disconnect packet id = %d, want %d", packetID, mcproto.LoginDisconnectPacketID)
	}
	reasonJSON, remaining := parseStringPayload(t, payload)
	if len(remaining) != 0 {
		t.Fatalf("login disconnect has %d trailing bytes", len(remaining))
	}
	var reason mcproto.StatusChatComponent
	if err := json.Unmarshal([]byte(reasonJSON), &reason); err != nil {
		t.Fatalf("Unmarshal login disconnect reason: %v", err)
	}
	return reasonJSON, reason
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

func assertMetricAbsentOrZero(t *testing.T, server *Server, name string, labels map[string]string) {
	t.Helper()
	if got, ok := metricValue(t, server, name, labels); ok && got != 0 {
		t.Fatalf("metric %s%v = %v, want absent or 0", name, labels, got)
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
