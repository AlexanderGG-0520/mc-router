package bedrockproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/bedrockroute"
	"github.com/sandertv/gophertunnel/minecraft"
)

type Config struct {
	Listen             string
	DefaultBackend     string
	Routes             []bedrockroute.Route
	BackendDialTimeout time.Duration
}

type Proxy struct {
	cfg      Config
	logger   *slog.Logger
	router   *bedrockroute.Router
	listener *minecraft.Listener

	closeOnce sync.Once
	wg        sync.WaitGroup
}

func New(cfg Config, logger *slog.Logger) (*Proxy, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.BackendDialTimeout <= 0 {
		return nil, errors.New("backend dial timeout must be positive")
	}
	router, err := bedrockroute.NewRouter(cfg.DefaultBackend, cfg.Routes)
	if err != nil {
		return nil, err
	}
	listener, err := minecraft.ListenConfig{
		AuthenticationDisabled: true,
		ErrorLog:               logger.With("component", "bedrock_host_proxy"),
	}.Listen("raknet", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen Bedrock host proxy %q: %w", cfg.Listen, err)
	}
	return &Proxy{
		cfg:      cfg,
		logger:   logger,
		router:   router,
		listener: listener,
	}, nil
}

func (p *Proxy) Addr() string {
	if p == nil || p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

func (p *Proxy) Serve(ctx context.Context) error {
	p.logger.Info("bedrock_host_proxy_started", "address", p.listener.Addr().String())
	defer p.logger.Info("bedrock_host_proxy_stopped")

	go func() {
		<-ctx.Done()
		_ = p.Close()
	}()

	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				p.wg.Wait()
				return nil
			}
			p.logger.Warn("bedrock_host_proxy_accept_failed", "error", err)
			continue
		}
		clientConn := conn.(*minecraft.Conn)
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handleConn(ctx, clientConn)
		}()
	}
}

func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		_ = p.listener.Close()
	})
	return nil
}

func (p *Proxy) handleConn(ctx context.Context, clientConn *minecraft.Conn) {
	requestedAddress := clientConn.ClientData().ServerAddress
	selection := p.router.Select(requestedAddress)
	p.logger.Info(
		"bedrock_host_proxy_route_selected",
		"remote", clientConn.RemoteAddr().String(),
		"requested_address", requestedAddress,
		"route", selection.RouteName,
		"matched", selection.Matched,
	)

	dialCtx, cancel := context.WithTimeout(ctx, p.cfg.BackendDialTimeout)
	defer cancel()

	serverConn, err := minecraft.Dialer{
		ClientData:                 clientConn.ClientData(),
		IdentityData:               clientConn.IdentityData(),
		KeepXBLIdentityData:        true,
		DisconnectOnInvalidPackets: false,
		DisconnectOnUnknownPackets: false,
		ErrorLog:                   p.logger.With("component", "bedrock_host_proxy_dialer"),
	}.DialContext(dialCtx, "raknet", selection.Backend)
	if err != nil {
		p.logger.Warn("bedrock_host_proxy_backend_dial_failed", "backend", selection.Backend, "error", err)
		_ = p.listener.Disconnect(clientConn, "Bedrock backend unavailable.")
		return
	}
	defer serverConn.Close()
	defer p.listener.Disconnect(clientConn, "connection lost")

	if err := p.spawn(clientConn, serverConn); err != nil {
		p.logger.Debug("bedrock_host_proxy_spawn_failed", "error", err)
		return
	}
	p.bridge(clientConn, serverConn)
}

func (p *Proxy) spawn(clientConn *minecraft.Conn, serverConn *minecraft.Conn) error {
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := clientConn.StartGame(serverConn.GameData()); err != nil {
			errCh <- fmt.Errorf("start client game: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := serverConn.DoSpawn(); err != nil {
			errCh <- fmt.Errorf("spawn backend connection: %w", err)
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Proxy) bridge(clientConn *minecraft.Conn, serverConn *minecraft.Conn) {
	var wg sync.WaitGroup
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = clientConn.Close()
			_ = serverConn.Close()
		})
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		for {
			pk, err := clientConn.ReadPacket()
			if err != nil {
				return
			}
			if err := serverConn.WritePacket(pk); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		for {
			pk, err := serverConn.ReadPacket()
			if err != nil {
				return
			}
			if err := clientConn.WritePacket(pk); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}
