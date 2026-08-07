package bedrockproxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/bedrockroute"
	"github.com/sandertv/gophertunnel/minecraft"
)

func TestProxyRoutesConnectionByRequestedHost(t *testing.T) {
	hub := startTestBedrockBackend(t, "hub")
	creative := startTestBedrockBackend(t, "creative")

	proxy := startTestProxy(t, Config{
		Listen:             "127.0.0.1:0",
		DefaultBackend:     hub.addr(),
		BackendDialTimeout: 5 * time.Second,
		Routes: []bedrockroute.Route{
			{Name: "creative", Hosts: []string{"LOCALHOST"}, Backend: creative.addr()},
		},
	})

	client, err := minecraft.Dialer{}.Dial("raknet", proxyAddressWithHost(t, proxy, "localhost"))
	if err != nil {
		t.Fatalf("client dial proxy: %v", err)
	}
	defer client.Close()
	if err := client.DoSpawn(); err != nil {
		t.Fatalf("client spawn: %v", err)
	}

	assertBackendAccepted(t, creative, "creative")
	assertBackendNotAccepted(t, hub)
}

func TestProxyFallsBackToDefaultBackendForUnknownHost(t *testing.T) {
	hub := startTestBedrockBackend(t, "hub")
	creative := startTestBedrockBackend(t, "creative")

	proxy := startTestProxy(t, Config{
		Listen:             "127.0.0.1:0",
		DefaultBackend:     hub.addr(),
		BackendDialTimeout: 5 * time.Second,
		Routes: []bedrockroute.Route{
			{Name: "creative", Hosts: []string{"localhost"}, Backend: creative.addr()},
		},
	})

	client, err := minecraft.Dialer{}.Dial("raknet", proxy.Addr())
	if err != nil {
		t.Fatalf("client dial proxy: %v", err)
	}
	defer client.Close()
	if err := client.DoSpawn(); err != nil {
		t.Fatalf("client spawn: %v", err)
	}

	assertBackendAccepted(t, hub, "hub")
	assertBackendNotAccepted(t, creative)
}

func startTestProxy(t *testing.T, cfg Config) *Proxy {
	t.Helper()
	proxy, err := New(cfg, discardProxyLogger())
	if err != nil {
		t.Fatalf("New proxy: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- proxy.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = proxy.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("proxy Serve returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("proxy did not stop")
		}
	})
	return proxy
}

func proxyAddressWithHost(t *testing.T, proxy *Proxy, host string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(proxy.Addr())
	if err != nil {
		t.Fatalf("split proxy address %q: %v", proxy.Addr(), err)
	}
	return net.JoinHostPort(host, port)
}

func assertBackendAccepted(t *testing.T, backend *testBedrockBackend, want string) {
	t.Helper()
	select {
	case got := <-backend.accepted:
		if got != want {
			t.Fatalf("accepted backend = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("backend %q did not receive connection", want)
	}
}

func assertBackendNotAccepted(t *testing.T, backend *testBedrockBackend) {
	t.Helper()
	select {
	case got := <-backend.accepted:
		t.Fatalf("backend unexpectedly accepted connection %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

type testBedrockBackend struct {
	listener *minecraft.Listener
	accepted chan string
}

func startTestBedrockBackend(t *testing.T, name string) *testBedrockBackend {
	t.Helper()
	listener, err := minecraft.ListenConfig{
		AuthenticationDisabled: true,
		ErrorLog:               discardProxyLogger(),
	}.Listen("raknet", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend %s: %v", name, err)
	}
	backend := &testBedrockBackend{
		listener: listener,
		accepted: make(chan string, 1),
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		mcConn := conn.(*minecraft.Conn)
		defer mcConn.Close()
		if err := mcConn.StartGame(minecraft.GameData{WorldName: name}); err != nil {
			return
		}
		backend.accepted <- name
		for {
			if _, err := mcConn.ReadPacket(); err != nil {
				return
			}
		}
	}()
	return backend
}

func (b *testBedrockBackend) addr() string {
	return b.listener.Addr().String()
}

func discardProxyLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
