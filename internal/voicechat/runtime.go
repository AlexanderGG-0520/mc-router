package voicechat

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
)

type Config struct {
	Listen             string
	RegistrationListen string
	RegistrationTTL    time.Duration
	RequestTimeout     time.Duration
	MaxRegistrations   int
	IdleTimeout        time.Duration
	BackendDialTimeout time.Duration
	MaxSessions        int
	MaxPacketSize      int
	Backends           map[string]BackendConfig
}

type BackendConfig struct {
	UDP   string
	Token string
}

type Runtime struct {
	cfg     Config
	logger  *slog.Logger
	metrics *gatewaymetrics.Recorder

	listener *net.UDPConn
	control  *http.Server

	mu            sync.Mutex
	closed        bool
	registrations map[UUID]registration
	sessions      map[string]*dynamicSession
	backends      map[string]backend
	tokens        []backendToken
	closeCh       chan struct{}

	closeOnce sync.Once
	wg        sync.WaitGroup
}

type backend struct {
	id   string
	addr *net.UDPAddr
}

type backendToken struct {
	backendID string
	token     []byte
}

type registration struct {
	PlayerUUID UUID
	BackendID  string
	OwnerID    string
	LeaseID    string
	Generation uint64
	ExpiresAt  time.Time
}

type dynamicSession struct {
	runtime    *Runtime
	key        string
	playerUUID UUID
	clientAddr *net.UDPAddr
	backendID  string
	leaseID    string
	generation uint64
	backend    *net.UDPConn

	mu           sync.Mutex
	lastActivity time.Time
	closed       bool
	write        func([]byte) (int, error)
	closeOnce    sync.Once
}

func NewRuntime(cfg Config, logger *slog.Logger, metrics *gatewaymetrics.Recorder) (*Runtime, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.RegistrationTTL <= 0 {
		return nil, errors.New("registration TTL must be positive")
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("request timeout must be positive")
	}
	if cfg.MaxRegistrations <= 0 {
		return nil, errors.New("max registrations must be positive")
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
	if len(cfg.Backends) == 0 {
		return nil, errors.New("at least one voicechat backend is required")
	}

	listenAddr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("resolve voicechat listen address %q: %w", cfg.Listen, err)
	}
	listener, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen voicechat UDP %q: %w", cfg.Listen, err)
	}

	r := &Runtime{
		cfg:           cfg,
		logger:        logger,
		metrics:       metrics,
		listener:      listener,
		registrations: make(map[UUID]registration),
		sessions:      make(map[string]*dynamicSession),
		backends:      make(map[string]backend),
		closeCh:       make(chan struct{}),
	}
	for id, cfg := range cfg.Backends {
		addr, err := net.ResolveUDPAddr("udp", cfg.UDP)
		if err != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("resolve voicechat backend %q: %w", id, err)
		}
		if err := validateBackendAddr(addr); err != nil {
			_ = listener.Close()
			return nil, fmt.Errorf("invalid voicechat backend %q: %w", id, err)
		}
		r.backends[id] = backend{id: id, addr: addr}
		r.tokens = append(r.tokens, backendToken{backendID: id, token: []byte(cfg.Token)})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/voicechat/registrations/", r.handleRegistration)
	r.control = &http.Server{
		Addr:              cfg.RegistrationListen,
		Handler:           http.TimeoutHandler(mux, cfg.RequestTimeout, "request timeout\n"),
		ReadHeaderTimeout: cfg.RequestTimeout,
	}
	return r, nil
}

func (r *Runtime) UDPAddr() string {
	if r == nil || r.listener == nil {
		return ""
	}
	return r.listener.LocalAddr().String()
}

func (r *Runtime) Serve(ctx context.Context) error {
	regListener, err := net.Listen("tcp", r.cfg.RegistrationListen)
	if err != nil {
		_ = r.Close()
		return fmt.Errorf("listen voicechat registration API %q: %w", r.cfg.RegistrationListen, err)
	}
	r.logger.Info("voicechat_registration_api_started", "address", regListener.Addr().String())
	r.logger.Info("voicechat_udp_started", "address", r.listener.LocalAddr().String())
	defer r.logger.Info("voicechat_runtime_stopped")

	errCh := make(chan error, 2)
	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		if err := r.control.Serve(regListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	go func() {
		defer r.wg.Done()
		errCh <- r.serveUDP(ctx)
	}()
	r.wg.Add(1)
	go r.expireLoop(ctx)

	select {
	case <-ctx.Done():
		_ = r.Close()
	case err := <-errCh:
		_ = r.Close()
		r.wg.Wait()
		return err
	}
	r.wg.Wait()
	return nil
}

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		sessions := make([]*dynamicSession, 0, len(r.sessions))
		for _, sess := range r.sessions {
			sessions = append(sessions, sess)
		}
		r.sessions = make(map[string]*dynamicSession)
		r.registrations = make(map[UUID]registration)
		r.mu.Unlock()

		close(r.closeCh)
		_ = r.listener.Close()
		_ = r.control.Close()
		for _, sess := range sessions {
			sess.close(gatewaymetrics.VoiceChatSessionCloseReasonShutdown)
		}
		if r.metrics != nil {
			r.metrics.VoiceChatRegistrationsSet(0)
		}
	})
	return nil
}

func (r *Runtime) serveUDP(ctx context.Context) error {
	buf := make([]byte, r.cfg.MaxPacketSize+1)
	for {
		n, clientAddr, err := r.listener.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) || r.isClosed() {
				return nil
			}
			r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, gatewaymetrics.VoiceChatPacketResultDroppedReadError, 0)
			r.logger.Warn("voicechat_udp_read_failed", "error", err)
			continue
		}
		if n > r.cfg.MaxPacketSize {
			r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, gatewaymetrics.VoiceChatPacketResultDroppedMalformed, n)
			continue
		}
		payload := append([]byte(nil), buf[:n]...)
		if err := r.forwardClientPacket(ctx, clientAddr, payload); err != nil {
			r.logger.Debug("voicechat_client_packet_dropped", "remote", clientAddr.String(), "reason", lowCardinalityDropReason(err))
		}
	}
}

var (
	errUnknownRegistration = errors.New("voicechat registration not found")
	errExpiredRegistration = errors.New("voicechat registration expired")
	errSessionLimit        = errors.New("voicechat session limit reached")
	errBackendMissing      = errors.New("voicechat backend missing")
)

func (r *Runtime) forwardClientPacket(ctx context.Context, clientAddr *net.UDPAddr, payload []byte) error {
	packet, err := ParseClientPacket(payload)
	if err != nil {
		r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, gatewaymetrics.VoiceChatPacketResultDroppedMalformed, len(payload))
		return err
	}
	reg, err := r.lookupRegistration(packet.PlayerUUID)
	if err != nil {
		result := gatewaymetrics.VoiceChatPacketResultDroppedUnknownSession
		if errors.Is(err, errExpiredRegistration) {
			result = gatewaymetrics.VoiceChatPacketResultDroppedExpiredRegistration
		}
		r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, result, len(payload))
		return err
	}
	sess, created, err := r.sessionFor(ctx, packet.PlayerUUID, clientAddr, reg)
	if err != nil {
		if errors.Is(err, errSessionLimit) {
			r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, gatewaymetrics.VoiceChatPacketResultDroppedSessionLimit, len(payload))
		} else {
			r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, gatewaymetrics.VoiceChatPacketResultDroppedWriteError, len(payload))
		}
		return err
	}
	if created {
		r.logger.Debug("voicechat_udp_session_created", "remote", clientAddr.String())
	}
	sess.touch()
	if _, err := sess.writeBackend(payload); err != nil {
		r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, gatewaymetrics.VoiceChatPacketResultDroppedWriteError, len(payload))
		r.closeSession(sess, gatewaymetrics.VoiceChatSessionCloseReasonBackendError)
		return err
	}
	r.packet(gatewaymetrics.VoiceChatPacketDirectionClientToBackend, gatewaymetrics.VoiceChatPacketResultForwarded, len(payload))
	return nil
}

func (r *Runtime) lookupRegistration(uuid UUID) (registration, error) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.registrations[uuid]
	if !ok {
		return registration{}, errUnknownRegistration
	}
	if now.After(reg.ExpiresAt) {
		delete(r.registrations, uuid)
		if r.metrics != nil {
			r.metrics.VoiceChatRegistrationsSet(len(r.registrations))
			r.metrics.VoiceChatRegistrationEvent(gatewaymetrics.VoiceChatRegistrationResultExpired)
		}
		return registration{}, errExpiredRegistration
	}
	return reg, nil
}

func (r *Runtime) sessionFor(ctx context.Context, uuid UUID, clientAddr *net.UDPAddr, reg registration) (*dynamicSession, bool, error) {
	key := dynamicSessionKey(uuid, clientAddr)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, false, net.ErrClosed
	}
	if sess := r.sessions[key]; sess != nil {
		if sess.backendID == reg.BackendID && sess.leaseID == reg.LeaseID && sess.generation == reg.Generation {
			r.mu.Unlock()
			return sess, false, nil
		}
		delete(r.sessions, key)
		r.mu.Unlock()
		sess.close(gatewaymetrics.VoiceChatSessionCloseReasonReassigned)
	} else {
		r.mu.Unlock()
	}

	r.mu.Lock()
	if len(r.sessions) >= r.cfg.MaxSessions {
		r.mu.Unlock()
		return nil, false, errSessionLimit
	}
	backend, ok := r.backends[reg.BackendID]
	r.mu.Unlock()
	if !ok {
		return nil, false, errBackendMissing
	}

	dialCtx, cancel := context.WithTimeout(ctx, r.cfg.BackendDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "udp", backend.addr.String())
	if err != nil {
		return nil, false, err
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, false, errors.New("backend dial did not return UDP connection")
	}
	sess := &dynamicSession{
		runtime:      r,
		key:          key,
		playerUUID:   uuid,
		clientAddr:   cloneUDPAddr(clientAddr),
		backendID:    reg.BackendID,
		leaseID:      reg.LeaseID,
		generation:   reg.Generation,
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
	if r.metrics != nil {
		r.metrics.VoiceChatSessionCreated()
	}
	r.wg.Add(1)
	go sess.readBackend()
	return sess, true, nil
}

func (s *dynamicSession) readBackend() {
	defer s.runtime.wg.Done()
	buf := make([]byte, s.runtime.cfg.MaxPacketSize+1)
	for {
		n, err := s.backend.Read(buf)
		if err != nil {
			if s.isClosed() || s.runtime.isClosed() {
				return
			}
			s.runtime.packet(gatewaymetrics.VoiceChatPacketDirectionBackendToClient, gatewaymetrics.VoiceChatPacketResultDroppedReadError, 0)
			s.runtime.closeSession(s, gatewaymetrics.VoiceChatSessionCloseReasonBackendError)
			return
		}
		if n > s.runtime.cfg.MaxPacketSize {
			s.runtime.packet(gatewaymetrics.VoiceChatPacketDirectionBackendToClient, gatewaymetrics.VoiceChatPacketResultDroppedMalformed, n)
			continue
		}
		if !s.runtime.sessionIsCurrent(s) {
			return
		}
		payload := append([]byte(nil), buf[:n]...)
		if _, err := s.runtime.listener.WriteToUDP(payload, s.clientAddr); err != nil {
			s.runtime.packet(gatewaymetrics.VoiceChatPacketDirectionBackendToClient, gatewaymetrics.VoiceChatPacketResultDroppedWriteError, len(payload))
			if s.runtime.isClosed() || errors.Is(err, net.ErrClosed) {
				return
			}
			s.runtime.closeSession(s, gatewaymetrics.VoiceChatSessionCloseReasonBackendError)
			return
		}
		s.touch()
		s.runtime.packet(gatewaymetrics.VoiceChatPacketDirectionBackendToClient, gatewaymetrics.VoiceChatPacketResultForwarded, len(payload))
	}
}

func (r *Runtime) expireLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(cleanupInterval(minDuration(r.cfg.IdleTimeout, r.cfg.RegistrationTTL)))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.closeCh:
			return
		case <-ticker.C:
			r.expireRegistrations(time.Now())
			for _, sess := range r.snapshotSessions() {
				if time.Since(sess.lastSeen()) >= r.cfg.IdleTimeout {
					r.closeSession(sess, gatewaymetrics.VoiceChatSessionCloseReasonIdleTimeout)
				}
			}
		}
	}
}

func (r *Runtime) expireRegistrations(now time.Time) {
	var expired []UUID
	r.mu.Lock()
	for uuid, reg := range r.registrations {
		if now.After(reg.ExpiresAt) {
			delete(r.registrations, uuid)
			expired = append(expired, uuid)
		}
	}
	count := len(r.registrations)
	r.mu.Unlock()
	if len(expired) == 0 {
		return
	}
	if r.metrics != nil {
		r.metrics.VoiceChatRegistrationsSet(count)
		for range expired {
			r.metrics.VoiceChatRegistrationEvent(gatewaymetrics.VoiceChatRegistrationResultExpired)
		}
	}
	for _, uuid := range expired {
		r.closeSessionsForUUID(uuid, gatewaymetrics.VoiceChatSessionCloseReasonRegistrationExpired)
	}
}

func (r *Runtime) handleRegistration(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	backendID, ok := r.authenticate(req)
	if !ok {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultAuthFailed)
		http.Error(w, `{"error":"unauthorized"}`+"\n", http.StatusUnauthorized)
		return
	}

	uuid, action, ok := parseRegistrationPath(req.URL.Path)
	if !ok {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultMalformed)
		http.Error(w, `{"error":"not found"}`+"\n", http.StatusNotFound)
		return
	}

	req.Body = http.MaxBytesReader(w, req.Body, 4096)
	defer req.Body.Close()
	switch {
	case req.Method == http.MethodPut && action == "":
		r.handlePutRegistration(w, req, backendID, uuid)
	case req.Method == http.MethodPost && action == "refresh":
		r.handleRefreshRegistration(w, req, backendID, uuid)
	case req.Method == http.MethodDelete && action == "":
		r.handleDeleteRegistration(w, req, backendID, uuid)
	default:
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultMalformed)
		http.Error(w, `{"error":"method not allowed"}`+"\n", http.StatusMethodNotAllowed)
	}
}

type putRegistrationRequest struct {
	OwnerID string `json:"ownerId"`
}

type leaseRequest struct {
	OwnerID string `json:"ownerId"`
	LeaseID string `json:"leaseId"`
}

type registrationResponse struct {
	BackendID string `json:"backendId"`
	LeaseID   string `json:"leaseId"`
	ExpiresAt string `json:"expiresAt"`
}

func (r *Runtime) handlePutRegistration(w http.ResponseWriter, req *http.Request, backendID string, uuid UUID) {
	var body putRegistrationRequest
	if err := decodeJSON(req.Body, &body); err != nil || strings.TrimSpace(body.OwnerID) == "" {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultMalformed)
		http.Error(w, `{"error":"malformed request"}`+"\n", http.StatusBadRequest)
		return
	}
	leaseID, err := newLeaseID()
	if err != nil {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultFailed)
		http.Error(w, `{"error":"lease generation failed"}`+"\n", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	reg := registration{
		PlayerUUID: uuid,
		BackendID:  backendID,
		OwnerID:    body.OwnerID,
		LeaseID:    leaseID,
		ExpiresAt:  now.Add(r.cfg.RegistrationTTL),
	}
	var replaced *registration
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultFailed)
		http.Error(w, `{"error":"closed"}`+"\n", http.StatusServiceUnavailable)
		return
	}
	if existing, ok := r.registrations[uuid]; ok {
		reg.Generation = existing.Generation + 1
		replaced = &existing
	} else {
		if len(r.registrations) >= r.cfg.MaxRegistrations {
			r.mu.Unlock()
			r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultLimit)
			http.Error(w, `{"error":"registration limit reached"}`+"\n", http.StatusTooManyRequests)
			return
		}
		reg.Generation = 1
	}
	r.registrations[uuid] = reg
	count := len(r.registrations)
	r.mu.Unlock()

	if r.metrics != nil {
		r.metrics.VoiceChatRegistrationsSet(count)
	}
	if replaced != nil {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultReplaced)
		r.closeSessionsForUUID(uuid, gatewaymetrics.VoiceChatSessionCloseReasonReassigned)
		if replaced.BackendID != backendID && r.metrics != nil {
			r.metrics.VoiceChatBackendSwitch()
		}
		r.logger.Debug("voicechat_registration_replaced")
	} else {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultCreated)
		r.logger.Debug("voicechat_registration_created")
	}
	writeRegistrationResponse(w, http.StatusOK, reg)
}

func (r *Runtime) handleRefreshRegistration(w http.ResponseWriter, req *http.Request, backendID string, uuid UUID) {
	var body leaseRequest
	if err := decodeJSON(req.Body, &body); err != nil || strings.TrimSpace(body.OwnerID) == "" || strings.TrimSpace(body.LeaseID) == "" {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultMalformed)
		http.Error(w, `{"error":"malformed request"}`+"\n", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	reg, ok := r.registrations[uuid]
	if !ok || reg.BackendID != backendID || reg.OwnerID != body.OwnerID || reg.LeaseID != body.LeaseID {
		r.mu.Unlock()
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultStaleLease)
		http.Error(w, `{"error":"lease not found"}`+"\n", http.StatusConflict)
		return
	}
	reg.ExpiresAt = time.Now().Add(r.cfg.RegistrationTTL)
	r.registrations[uuid] = reg
	r.mu.Unlock()
	r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultRefreshed)
	writeRegistrationResponse(w, http.StatusOK, reg)
}

func (r *Runtime) handleDeleteRegistration(w http.ResponseWriter, req *http.Request, backendID string, uuid UUID) {
	var body leaseRequest
	if err := decodeJSON(req.Body, &body); err != nil || strings.TrimSpace(body.OwnerID) == "" || strings.TrimSpace(body.LeaseID) == "" {
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultMalformed)
		http.Error(w, `{"error":"malformed request"}`+"\n", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	reg, ok := r.registrations[uuid]
	if !ok || reg.BackendID != backendID || reg.OwnerID != body.OwnerID || reg.LeaseID != body.LeaseID {
		r.mu.Unlock()
		r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultStaleLease)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	delete(r.registrations, uuid)
	count := len(r.registrations)
	r.mu.Unlock()
	if r.metrics != nil {
		r.metrics.VoiceChatRegistrationsSet(count)
	}
	r.closeSessionsForUUID(uuid, gatewaymetrics.VoiceChatSessionCloseReasonUnregistered)
	r.registrationEvent(gatewaymetrics.VoiceChatRegistrationResultDeleted)
	w.WriteHeader(http.StatusNoContent)
}

func (r *Runtime) authenticate(req *http.Request) (string, bool) {
	const prefix = "Bearer "
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := []byte(strings.TrimSpace(strings.TrimPrefix(auth, prefix)))
	for _, candidate := range r.tokens {
		if len(token) == len(candidate.token) && subtle.ConstantTimeCompare(token, candidate.token) == 1 {
			return candidate.backendID, true
		}
	}
	return "", false
}

func parseRegistrationPath(path string) (UUID, string, bool) {
	const prefix = "/v1/voicechat/registrations/"
	if !strings.HasPrefix(path, prefix) {
		return UUID{}, "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 1 && len(parts) != 2 {
		return UUID{}, "", false
	}
	uuid, err := ParseUUID(parts[0])
	if err != nil || uuid.String() != parts[0] {
		return UUID{}, "", false
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	return uuid, action, true
}

func decodeJSON(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func writeRegistrationResponse(w http.ResponseWriter, status int, reg registration) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(registrationResponse{
		BackendID: reg.BackendID,
		LeaseID:   reg.LeaseID,
		ExpiresAt: reg.ExpiresAt.Format(time.RFC3339Nano),
	})
}

func newLeaseID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (r *Runtime) snapshotSessions() []*dynamicSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	sessions := make([]*dynamicSession, 0, len(r.sessions))
	for _, sess := range r.sessions {
		sessions = append(sessions, sess)
	}
	return sessions
}

func (r *Runtime) closeSessionsForUUID(uuid UUID, reason string) {
	var sessions []*dynamicSession
	r.mu.Lock()
	for key, sess := range r.sessions {
		if sess.playerUUID == uuid {
			delete(r.sessions, key)
			sessions = append(sessions, sess)
		}
	}
	r.mu.Unlock()
	for _, sess := range sessions {
		sess.close(reason)
	}
}

func (r *Runtime) closeSession(sess *dynamicSession, reason string) {
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

func (r *Runtime) sessionIsCurrent(sess *dynamicSession) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed && r.sessions[sess.key] == sess
}

func (r *Runtime) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (s *dynamicSession) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

func (s *dynamicSession) writeBackend(payload []byte) (int, error) {
	s.mu.Lock()
	write := s.write
	s.mu.Unlock()
	return write(payload)
}

func (s *dynamicSession) lastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivity
}

func (s *dynamicSession) close(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.backend.Close()
		if s.runtime.metrics != nil {
			s.runtime.metrics.VoiceChatSessionClosed(reason)
		}
	})
}

func (s *dynamicSession) closeWithoutMetric() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.backend.Close()
	})
}

func (s *dynamicSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (r *Runtime) packet(direction string, result string, bytes int) {
	if r.metrics != nil {
		r.metrics.VoiceChatPacket(direction, result, bytes)
	}
}

func (r *Runtime) registrationEvent(result string) {
	if r.metrics != nil {
		r.metrics.VoiceChatRegistrationEvent(result)
	}
}

func dynamicSessionKey(uuid UUID, addr *net.UDPAddr) string {
	return uuid.String() + "|" + addr.String()
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

func cleanupInterval(timeout time.Duration) time.Duration {
	interval := timeout / 2
	if interval < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	if interval > time.Second {
		return time.Second
	}
	return interval
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func lowCardinalityDropReason(err error) string {
	switch {
	case errors.Is(err, errUnknownRegistration):
		return "unknown_registration"
	case errors.Is(err, errExpiredRegistration):
		return "expired_registration"
	case errors.Is(err, errSessionLimit):
		return "session_limit"
	default:
		return "invalid_or_io_error"
	}
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
