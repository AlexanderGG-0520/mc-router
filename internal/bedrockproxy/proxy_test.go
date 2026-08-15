package bedrockproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/bedrockroute"
	"github.com/sandertv/gophertunnel/minecraft"
)

var errTestBackendDial = errors.New("test backend dial stopped")

func TestProxyRoutesConnectionByRequestedHost(t *testing.T) {
	const (
		hubBackend      = "hub.test:19132"
		creativeBackend = "creative.test:19132"
	)
	selected, configureDial := captureBackendDial()
	proxy := startTestProxy(t, Config{
		Listen:             "127.0.0.1:0",
		DefaultBackend:     hubBackend,
		BackendDialTimeout: 5 * time.Second,
		Routes: []bedrockroute.Route{
			{Name: "creative", Hosts: []string{"127.0.0.1:19132"}, Backend: creativeBackend},
		},
	}, configureDial)

	dialProxyAsync(t, proxy.Addr())
	assertSelectedBackend(t, selected, creativeBackend)
}

func TestProxyFallsBackToDefaultBackendForUnknownHost(t *testing.T) {
	const (
		hubBackend      = "hub.test:19132"
		creativeBackend = "creative.test:19132"
	)
	selected, configureDial := captureBackendDial()
	proxy := startTestProxy(t, Config{
		Listen:             "127.0.0.1:0",
		DefaultBackend:     hubBackend,
		BackendDialTimeout: 5 * time.Second,
		Routes: []bedrockroute.Route{
			{Name: "creative", Hosts: []string{"creative.play.example.com"}, Backend: creativeBackend},
		},
	}, configureDial)

	dialProxyAsync(t, proxy.Addr())
	assertSelectedBackend(t, selected, hubBackend)
}

func TestProxyCloseStopsActiveConnection(t *testing.T) {
	proxy, err := New(Config{
		Listen:             "127.0.0.1:0",
		DefaultBackend:     "hub.test:19132",
		BackendDialTimeout: 30 * time.Second,
	}, discardProxyLogger())
	if err != nil {
		t.Fatalf("New proxy: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Close() })

	dialStarted := make(chan struct{}, 1)
	proxy.dialBackend = func(ctx context.Context, _ *minecraft.Conn, _ string) (*minecraft.Conn, error) {
		dialStarted <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	done := make(chan error, 1)
	go func() {
		done <- proxy.Serve(context.Background())
	}()
	clientCtx, cancelClient := context.WithCancel(context.Background())
	clientDone := dialProxy(clientCtx, proxy.Addr())

	select {
	case <-dialStarted:
	case <-time.After(5 * time.Second):
		cancelClient()
		t.Fatal("backend dial did not start")
	}

	if err := proxy.Close(); err != nil {
		cancelClient()
		t.Fatalf("proxy Close returned error: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			cancelClient()
			t.Fatalf("proxy Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		cancelClient()
		t.Fatal("proxy did not stop with an active connection")
	}

	cancelClient()
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client dial did not stop after cancellation")
	}
}

func startTestProxy(t *testing.T, cfg Config, configure ...func(*Proxy)) *Proxy {
	t.Helper()
	proxy, err := New(cfg, discardProxyLogger())
	if err != nil {
		t.Fatalf("New proxy: %v", err)
	}
	for _, configureProxy := range configure {
		configureProxy(proxy)
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

func captureBackendDial() (<-chan string, func(*Proxy)) {
	selected := make(chan string, 1)
	return selected, func(proxy *Proxy) {
		proxy.dialBackend = func(ctx context.Context, _ *minecraft.Conn, backend string) (*minecraft.Conn, error) {
			select {
			case selected <- backend:
				return nil, errTestBackendDial
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
}

func assertSelectedBackend(t *testing.T, selected <-chan string, want string) {
	t.Helper()
	select {
	case got := <-selected:
		if got != want {
			t.Fatalf("selected backend = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("backend %q was not selected", want)
	}
}

func dialProxyAsync(t *testing.T, address string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := dialProxy(ctx, address)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("client dial did not stop")
		}
	})
}

func dialProxy(ctx context.Context, address string) <-chan error {
	done := make(chan error, 1)
	go func() {
		conn, err := (minecraft.Dialer{}).DialContext(ctx, "raknet", address)
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	return done
}

func discardProxyLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
