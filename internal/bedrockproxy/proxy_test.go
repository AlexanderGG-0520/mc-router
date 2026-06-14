package bedrockproxy

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/bedrockroute"
	"github.com/sandertv/gophertunnel/minecraft"
)

func TestProxyRoutesConnectionByRequestedHost(t *testing.T) {
	hub := startTestBedrockBackend(t, "hub")
	creative := startTestBedrockBackend(t, "creative")

	proxy, err := New(Config{
		Listen:             "127.0.0.1:0",
		DefaultBackend:     hub.addr(),
		BackendDialTimeout: 5 * time.Second,
		Routes: []bedrockroute.Route{
			{Name: "creative", Hosts: []string{"127.0.0.1"}, Backend: creative.addr()},
		},
	}, discardProxyLogger())
	if err != nil {
		t.Fatalf("New proxy: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- proxy.Serve(ctx)
	}()
	defer func() {
		cancel()
		_ = proxy.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("proxy Serve returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("proxy did not stop")
		}
	}()

	client, err := minecraft.Dialer{}.Dial("raknet", proxy.Addr())
	if err != nil {
		t.Fatalf("client dial proxy: %v", err)
	}
	defer client.Close()
	if err := client.DoSpawn(); err != nil {
		t.Fatalf("client spawn: %v", err)
	}

	select {
	case got := <-creative.accepted:
		if got != "creative" {
			t.Fatalf("accepted backend = %q, want creative", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("creative backend did not receive connection")
	}
	select {
	case got := <-hub.accepted:
		t.Fatalf("hub backend unexpectedly accepted connection %q", got)
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
		backend.accepted <- name
		mcConn := conn.(*minecraft.Conn)
		_ = mcConn.StartGame(minecraft.GameData{WorldName: name})
		_ = mcConn.Close()
	}()
	return backend
}

func (b *testBedrockBackend) addr() string {
	return b.listener.Addr().String()
}

func discardProxyLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
