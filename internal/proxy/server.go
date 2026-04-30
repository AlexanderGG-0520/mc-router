package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	"github.com/AlexanderGG-0520/mc-router/internal/router"
)

type Server struct {
	state  atomic.Pointer[serverState]
	logger *slog.Logger
	limits mcproto.Limits

	listenAddress string
	dialContext   dialContextFunc

	listenerMu sync.Mutex
	listener   net.Listener
	wg         sync.WaitGroup
}

type serverState struct {
	cfg    config.Config
	router *router.Router
}

type dialContextFunc func(ctx context.Context, network string, address string) (net.Conn, error)

const (
	reasonBackendClose       = "backend_close"
	reasonBackendDialFailed  = "backend_dial_failed"
	reasonBackendDialTimeout = "backend_dial_timeout"
	reasonClientClose        = "client_close"
	reasonContextCancelled   = "context_cancelled"
	reasonHandshakeMalformed = "handshake_malformed"
	reasonHandshakeTimeout   = "handshake_timeout"
	reasonInitialWriteFailed = "initial_write_failed"
	reasonRouteDenied        = "route_denied"
)

func NewServer(cfg config.Config, routeTable *router.Router, logger *slog.Logger) *Server {
	if routeTable == nil {
		panic("proxy: nil router")
	}
	dialer := net.Dialer{}
	s := &Server{
		logger:        logger,
		limits:        mcproto.DefaultLimits(),
		listenAddress: cfg.Listen,
		dialContext:   dialer.DialContext,
	}
	s.state.Store(&serverState{cfg: cfg, router: routeTable})
	return s
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
		s.logger.Error("reload_failed", "config", path, "error", err)
		return err
	}
	routeTable, err := router.New(cfg)
	if err != nil {
		s.logger.Error("reload_failed", "config", path, "error", err)
		return err
	}
	s.UpdateConfig(cfg, routeTable)
	s.logger.Info(
		"reload_success",
		"config", path,
		"routes", len(cfg.Routes),
		"listen", s.listenAddress,
		"configured_listen", cfg.Listen,
		"listen_change_ignored", cfg.Listen != s.listenAddress,
	)
	return nil
}

func (s *Server) UpdateConfig(cfg config.Config, routeTable *router.Router) {
	if routeTable == nil {
		panic("proxy: nil router")
	}
	s.state.Store(&serverState{cfg: cfg, router: routeTable})
}

func (s *Server) currentState() *serverState {
	state := s.state.Load()
	if state == nil {
		panic("proxy: server state is not initialized")
	}
	return state
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
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

	state := s.currentState()
	remoteAddr := client.RemoteAddr().String()
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
		s.logger.Info("connection rejected", "reason", reasonRouteDenied, "remote", remoteAddr, "server_address", routeAddress, "error", err)
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, state.cfg.BackendDialTimeout.Duration)
	defer cancel()
	backend, err := s.dialContext(dialCtx, "tcp", selection.Backend)
	if err != nil {
		reason := classifyDialError(err)
		s.logger.Warn("connection rejected", "reason", reason, "remote", remoteAddr, "server_address", routeAddress, "backend", selection.Backend, "error", err)
		return
	}
	defer backend.Close()

	if err := writeAll(backend, rawHandshake); err != nil {
		s.logger.Warn("connection rejected", "reason", reasonInitialWriteFailed, "remote", remoteAddr, "server_address", routeAddress, "backend", selection.Backend, "error", err)
		return
	}

	s.logger.Info(
		"proxying connection",
		"remote", remoteAddr,
		"server_address", routeAddress,
		"server_port", handshake.ServerPort,
		"next_state", handshake.NextState,
		"backend", selection.Backend,
		"matched_by", selection.MatchedBy,
	)
	result := s.proxy(ctx, client, backend)
	s.logger.Info(
		"proxy connection closed",
		"reason", result.reason,
		"remote", remoteAddr,
		"server_address", routeAddress,
		"backend", selection.Backend,
		"direction", result.direction,
		"bytes_copied", result.bytesCopied,
	)
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
