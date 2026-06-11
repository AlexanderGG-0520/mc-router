package udprelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
)

type Config struct {
	Listen             string
	Backend            string
	IdleTimeout        time.Duration
	BackendDialTimeout time.Duration
	MaxSessions        int
	MaxPacketSize      int
}

type Relay struct {
	cfg     Config
	logger  *slog.Logger
	metrics *gatewaymetrics.Recorder

	listener *net.UDPConn
	backend  *net.UDPAddr

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
	closeCh  chan struct{}

	closeOnce sync.Once
	wg        sync.WaitGroup
}

type session struct {
	relay      *Relay
	key        string
	clientAddr *net.UDPAddr
	backend    *net.UDPConn

	mu           sync.Mutex
	lastActivity time.Time
	closed       bool
	write        func([]byte) (int, error)
	closeOnce    sync.Once
}

func New(cfg Config, logger *slog.Logger, metrics *gatewaymetrics.Recorder) (*Relay, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.IdleTimeout <= 0 {
		return nil, errors.New("idle timeout must be positive")
	}
	if cfg.BackendDialTimeout <= 0 {
		return nil, errors.New("backend dial timeout must be positive")
	}
	if cfg.MaxSessions <= 0 {
		return nil, errors.New("max sessions must be positive")
	}
	if cfg.MaxPacketSize <= 0 {
		return nil, errors.New("max packet size must be positive")
	}

	listenAddr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP listen address %q: %w", cfg.Listen, err)
	}
	backendAddr, err := net.ResolveUDPAddr("udp", cfg.Backend)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP backend address %q: %w", cfg.Backend, err)
	}
	if err := validateBackendAddr(backendAddr); err != nil {
		return nil, fmt.Errorf("invalid UDP backend address %q: %w", cfg.Backend, err)
	}
	listener, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP %q: %w", cfg.Listen, err)
	}

	return &Relay{
		cfg:      cfg,
		logger:   logger,
		metrics:  metrics,
		listener: listener,
		backend:  backendAddr,
		sessions: make(map[string]*session),
		closeCh:  make(chan struct{}),
	}, nil
}

func (r *Relay) Addr() string {
	if r == nil || r.listener == nil {
		return ""
	}
	return r.listener.LocalAddr().String()
}

func (r *Relay) Serve(ctx context.Context) error {
	r.logger.Info("udp_relay_started", "address", r.listener.LocalAddr().String(), "backend", r.backend.String())
	defer r.logger.Info("udp_relay_stopped")

	r.wg.Add(1)
	go r.expireSessions(ctx)

	go func() {
		<-ctx.Done()
		_ = r.Close()
	}()

	buf := make([]byte, r.cfg.MaxPacketSize+1)
	for {
		n, clientAddr, err := r.listener.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) || r.isClosed() {
				r.wg.Wait()
				return nil
			}
			r.packet(gatewaymetrics.UDPPacketDirectionClientToBackend, gatewaymetrics.UDPPacketResultDroppedReadError, 0)
			r.logger.Warn("udp_relay_read_failed", "error", err)
			continue
		}
		if n > r.cfg.MaxPacketSize {
			r.packet(gatewaymetrics.UDPPacketDirectionClientToBackend, gatewaymetrics.UDPPacketResultDroppedReadError, n)
			r.logger.Debug("udp_relay_client_packet_too_large", "remote", clientAddr.String(), "bytes", n, "max_packet_size", r.cfg.MaxPacketSize)
			continue
		}
		payload := append([]byte(nil), buf[:n]...)
		if err := r.forwardToBackend(ctx, clientAddr, payload); err != nil {
			r.logger.Debug("udp_relay_client_packet_dropped", "remote", clientAddr.String(), "error", err)
		}
	}
}

func (r *Relay) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		sessions := make([]*session, 0, len(r.sessions))
		for _, sess := range r.sessions {
			sessions = append(sessions, sess)
		}
		r.sessions = make(map[string]*session)
		r.mu.Unlock()

		close(r.closeCh)
		_ = r.listener.Close()
		for _, sess := range sessions {
			sess.close(gatewaymetrics.UDPSessionCloseReasonShutdown)
		}
	})
	return nil
}

func (r *Relay) forwardToBackend(ctx context.Context, clientAddr *net.UDPAddr, payload []byte) error {
	sess, created, err := r.sessionForClient(ctx, clientAddr)
	if err != nil {
		if errors.Is(err, errSessionLimit) {
			r.packet(gatewaymetrics.UDPPacketDirectionClientToBackend, gatewaymetrics.UDPPacketResultDroppedSessionLimit, len(payload))
			r.logger.Debug("udp_relay_session_rejected", "reason", "session_limit", "remote", clientAddr.String())
			return err
		}
		r.packet(gatewaymetrics.UDPPacketDirectionClientToBackend, gatewaymetrics.UDPPacketResultDroppedWriteError, len(payload))
		return err
	}
	if created {
		r.logger.Debug("udp_relay_session_created", "remote", clientAddr.String())
	}
	sess.touch()
	if _, err := sess.writeBackend(payload); err != nil {
		r.packet(gatewaymetrics.UDPPacketDirectionClientToBackend, gatewaymetrics.UDPPacketResultDroppedWriteError, len(payload))
		r.closeSession(sess, gatewaymetrics.UDPSessionCloseReasonBackendError)
		return err
	}
	r.packet(gatewaymetrics.UDPPacketDirectionClientToBackend, gatewaymetrics.UDPPacketResultForwarded, len(payload))
	return nil
}

var errSessionLimit = errors.New("udp relay session limit reached")

func (r *Relay) sessionForClient(ctx context.Context, clientAddr *net.UDPAddr) (*session, bool, error) {
	key := clientAddr.String()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, false, net.ErrClosed
	}
	if sess := r.sessions[key]; sess != nil {
		r.mu.Unlock()
		return sess, false, nil
	}
	if len(r.sessions) >= r.cfg.MaxSessions {
		r.mu.Unlock()
		return nil, false, errSessionLimit
	}
	r.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(ctx, r.cfg.BackendDialTimeout)
	defer cancel()
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(dialCtx, "udp", r.backend.String())
	if err != nil {
		return nil, false, err
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, false, errors.New("backend dial did not return UDP connection")
	}

	sess := &session{
		relay:        r,
		key:          key,
		clientAddr:   cloneUDPAddr(clientAddr),
		backend:      udpConn,
		lastActivity: time.Now(),
	}
	sess.write = sess.backend.Write

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		sess.closeWithoutMetric()
		return nil, false, net.ErrClosed
	}
	if existing := r.sessions[key]; existing != nil {
		sess.closeWithoutMetric()
		return existing, false, nil
	}
	if len(r.sessions) >= r.cfg.MaxSessions {
		sess.closeWithoutMetric()
		return nil, false, errSessionLimit
	}
	r.sessions[key] = sess
	r.sessionCreated()
	r.wg.Add(1)
	go sess.readBackend()
	return sess, true, nil
}

func (s *session) readBackend() {
	defer s.relay.wg.Done()
	buf := make([]byte, s.relay.cfg.MaxPacketSize+1)
	for {
		n, err := s.backend.Read(buf)
		if err != nil {
			if s.isClosed() || s.relay.isClosed() {
				return
			}
			s.relay.packet(gatewaymetrics.UDPPacketDirectionBackendToClient, gatewaymetrics.UDPPacketResultDroppedReadError, 0)
			s.relay.logger.Debug("udp_relay_backend_read_failed", "remote", s.clientAddr.String(), "error", err)
			s.relay.closeSession(s, gatewaymetrics.UDPSessionCloseReasonBackendError)
			return
		}
		if n > s.relay.cfg.MaxPacketSize {
			s.relay.packet(gatewaymetrics.UDPPacketDirectionBackendToClient, gatewaymetrics.UDPPacketResultDroppedReadError, n)
			s.relay.logger.Debug("udp_relay_backend_packet_too_large", "remote", s.clientAddr.String(), "bytes", n, "max_packet_size", s.relay.cfg.MaxPacketSize)
			continue
		}
		if !s.relay.sessionIsCurrent(s) {
			return
		}
		payload := append([]byte(nil), buf[:n]...)
		if _, err := s.relay.listener.WriteToUDP(payload, s.clientAddr); err != nil {
			s.relay.packet(gatewaymetrics.UDPPacketDirectionBackendToClient, gatewaymetrics.UDPPacketResultDroppedWriteError, len(payload))
			if s.relay.isClosed() || errors.Is(err, net.ErrClosed) {
				return
			}
			s.relay.logger.Debug("udp_relay_client_write_failed", "remote", s.clientAddr.String(), "error", err)
			s.relay.closeSession(s, gatewaymetrics.UDPSessionCloseReasonBackendError)
			return
		}
		s.touch()
		s.relay.packet(gatewaymetrics.UDPPacketDirectionBackendToClient, gatewaymetrics.UDPPacketResultForwarded, len(payload))
	}
}

func (r *Relay) expireSessions(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(cleanupInterval(r.cfg.IdleTimeout))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.closeCh:
			return
		case <-ticker.C:
			now := time.Now()
			for _, sess := range r.snapshotSessions() {
				if now.Sub(sess.lastSeen()) >= r.cfg.IdleTimeout {
					r.logger.Debug("udp_relay_session_expired", "remote", sess.clientAddr.String())
					r.closeSession(sess, gatewaymetrics.UDPSessionCloseReasonIdleTimeout)
				}
			}
		}
	}
}

func cleanupInterval(idleTimeout time.Duration) time.Duration {
	interval := idleTimeout / 2
	if interval < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	if interval > time.Second {
		return time.Second
	}
	return interval
}

func (r *Relay) snapshotSessions() []*session {
	r.mu.Lock()
	defer r.mu.Unlock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, sess := range r.sessions {
		sessions = append(sessions, sess)
	}
	return sessions
}

func (r *Relay) closeSession(sess *session, reason string) {
	r.mu.Lock()
	if current := r.sessions[sess.key]; current == sess {
		delete(r.sessions, sess.key)
		r.mu.Unlock()
		sess.close(reason)
		return
	}
	r.mu.Unlock()
	sess.closeWithoutMetric()
}

func (r *Relay) sessionIsCurrent(sess *session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed && r.sessions[sess.key] == sess
}

func (r *Relay) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (s *session) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

func (s *session) writeBackend(payload []byte) (int, error) {
	s.mu.Lock()
	write := s.write
	s.mu.Unlock()
	return write(payload)
}

func (s *session) lastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivity
}

func (s *session) close(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.backend.Close()
		s.relay.sessionClosed(reason)
	})
}

func (s *session) closeWithoutMetric() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.backend.Close()
	})
}

func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (r *Relay) packet(direction string, result string, bytes int) {
	if r.metrics != nil {
		r.metrics.UDPPacket(direction, result, bytes)
	}
}

func (r *Relay) sessionCreated() {
	if r.metrics != nil {
		r.metrics.UDPSessionCreated()
	}
}

func (r *Relay) sessionClosed(reason string) {
	if r.metrics != nil {
		r.metrics.UDPSessionClosed(reason)
	}
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	cloned := *addr
	if addr.IP != nil {
		cloned.IP = append(net.IP(nil), addr.IP...)
	}
	return &cloned
}

func validateBackendAddr(addr *net.UDPAddr) error {
	if addr == nil || addr.IP == nil {
		return errors.New("resolved address is empty")
	}
	if addr.IP.IsUnspecified() {
		return errors.New("resolved address must not be unspecified")
	}
	if addr.IP.IsMulticast() {
		return errors.New("resolved address must not be multicast")
	}
	if ip4 := addr.IP.To4(); ip4 != nil && ip4.Equal(net.IPv4bcast) {
		return errors.New("resolved address must not be broadcast")
	}
	return nil
}
