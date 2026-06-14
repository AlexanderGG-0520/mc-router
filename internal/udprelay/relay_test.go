package udprelay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
	dto "github.com/prometheus/client_model/go"
)

func TestRelayForwardsClientDatagramAndBackendReply(t *testing.T) {
	backend := newUDPBackend(t)
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), []byte("hello"))
	got := backend.read(t)
	if !bytes.Equal(got.payload, []byte("hello")) {
		t.Fatalf("backend payload = %q, want hello", got.payload)
	}
	backend.reply(t, got.from, []byte("world"))
	if reply := readUDP(t, client); !bytes.Equal(reply, []byte("world")) {
		t.Fatalf("client reply = %q, want world", reply)
	}

	stop()
}

func TestRelayKeepsTwoClientsDistinctIncludingSameIPDifferentPorts(t *testing.T) {
	backend := newUDPBackend(t)
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	defer stop()
	clientA := newUDPClient(t)
	defer clientA.Close()
	clientB := newUDPClient(t)
	defer clientB.Close()

	sendUDP(t, clientA, relay.Addr(), []byte("a"))
	packetA := backend.read(t)
	sendUDP(t, clientB, relay.Addr(), []byte("b"))
	packetB := backend.read(t)
	if packetA.from.String() == packetB.from.String() {
		t.Fatalf("backend session source was reused across clients: %s", packetA.from)
	}
	if clientA.LocalAddr().(*net.UDPAddr).IP.String() != clientB.LocalAddr().(*net.UDPAddr).IP.String() {
		t.Fatalf("test clients are not on the same IP: %s != %s", clientA.LocalAddr(), clientB.LocalAddr())
	}

	backend.reply(t, packetB.from, []byte("reply-b"))
	backend.reply(t, packetA.from, []byte("reply-a"))
	if got := readUDP(t, clientA); !bytes.Equal(got, []byte("reply-a")) {
		t.Fatalf("client A reply = %q, want reply-a", got)
	}
	if got := readUDP(t, clientB); !bytes.Equal(got, []byte("reply-b")) {
		t.Fatalf("client B reply = %q, want reply-b", got)
	}
}

func TestRelayReusesSessionForSameEndpoint(t *testing.T) {
	backend := newUDPBackend(t)
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), []byte("one"))
	first := backend.read(t)
	sendUDP(t, client, relay.Addr(), []byte("two"))
	second := backend.read(t)
	if first.from.String() != second.from.String() {
		t.Fatalf("session source changed for same client: %s != %s", first.from, second.from)
	}
	if got := relaySessionCount(relay); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestNamedBackendResolverFallsBackToDefaultWhenNoRouteKeyAvailable(t *testing.T) {
	defaultBackend := newUDPBackend(t)
	creativeBackend := newUDPBackend(t)
	resolver, err := NewNamedBackendResolver(defaultBackend.addr(), []NamedBackendRoute{
		{Name: "creative", Backend: creativeBackend.addr()},
	})
	if err != nil {
		t.Fatalf("NewNamedBackendResolver: %v", err)
	}

	selection, err := resolver.ResolveBackend(context.Background(), nil, []byte("any bedrock datagram"))
	if err != nil {
		t.Fatalf("ResolveBackend: %v", err)
	}
	if selection.Addr.String() != defaultBackend.addr() {
		t.Fatalf("resolved backend = %s, want default %s", selection.Addr, defaultBackend.addr())
	}
}

func TestRelayKeepsSameClientOnInitiallySelectedBackend(t *testing.T) {
	backendA := newUDPBackend(t)
	backendB := newUDPBackend(t)
	resolver := &rotatingResolver{
		backends: []*net.UDPAddr{
			mustResolveUDPAddr(t, backendA.addr()),
			mustResolveUDPAddr(t, backendB.addr()),
		},
	}
	cfg := testConfig("")
	cfg.Resolver = resolver
	relay, _, stop := startTestRelayWithConfig(t, cfg, nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), []byte("one"))
	first := backendA.read(t)
	sendUDP(t, client, relay.Addr(), []byte("two"))
	second := backendA.read(t)
	assertNoBackendPacket(t, backendB)

	if first.from.String() != second.from.String() {
		t.Fatalf("session source changed for same client: %s != %s", first.from, second.from)
	}
	if got := resolver.callCount(); got != 1 {
		t.Fatalf("resolver calls = %d, want 1", got)
	}
}

func TestRelayForwardsToSelectedBackend(t *testing.T) {
	hubBackend := newUDPBackend(t)
	creativeBackend := newUDPBackend(t)
	resolver := &payloadResolver{
		defaultBackend:  mustResolveUDPAddr(t, hubBackend.addr()),
		creativeBackend: mustResolveUDPAddr(t, creativeBackend.addr()),
	}
	cfg := testConfig("")
	cfg.Resolver = resolver
	relay, _, stop := startTestRelayWithConfig(t, cfg, nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), []byte("creative"))
	packet := creativeBackend.read(t)
	if !bytes.Equal(packet.payload, []byte("creative")) {
		t.Fatalf("creative backend payload = %q", packet.payload)
	}
	assertNoBackendPacket(t, hubBackend)

	creativeBackend.reply(t, packet.from, []byte("creative-reply"))
	if got := readUDP(t, client); !bytes.Equal(got, []byte("creative-reply")) {
		t.Fatalf("client reply = %q, want creative-reply", got)
	}
}

func TestRelayExpiresIdleSessionsAndRefreshesActivity(t *testing.T) {
	backend := newUDPBackend(t)
	cfg := testConfig(backend.addr())
	cfg.IdleTimeout = 80 * time.Millisecond
	relay, _, stop := startTestRelayWithConfig(t, cfg, nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), []byte("one"))
	_ = backend.read(t)
	waitRelaySessionCount(t, relay, 1)
	time.Sleep(50 * time.Millisecond)
	sendUDP(t, client, relay.Addr(), []byte("two"))
	_ = backend.read(t)
	time.Sleep(50 * time.Millisecond)
	if got := relaySessionCount(relay); got != 1 {
		t.Fatalf("session expired despite activity refresh: %d", got)
	}
	waitRelaySessionCount(t, relay, 0)
}

func TestRelaySessionLimitRejectsOnlyNewSessions(t *testing.T) {
	backend := newUDPBackend(t)
	cfg := testConfig(backend.addr())
	cfg.MaxSessions = 1
	relay, _, stop := startTestRelayWithConfig(t, cfg, gatewaymetrics.NewRecorder(true))
	defer stop()
	clientA := newUDPClient(t)
	defer clientA.Close()
	clientB := newUDPClient(t)
	defer clientB.Close()

	sendUDP(t, clientA, relay.Addr(), []byte("first"))
	first := backend.read(t)
	sendUDP(t, clientB, relay.Addr(), []byte("dropped"))
	assertNoBackendPacket(t, backend)
	sendUDP(t, clientA, relay.Addr(), []byte("still-active"))
	second := backend.read(t)
	if first.from.String() != second.from.String() {
		t.Fatalf("existing session was not reused after limit: %s != %s", first.from, second.from)
	}
}

func TestRelayBackendSocketFailureRemovesOnlyAffectedSession(t *testing.T) {
	backend := newUDPBackend(t)
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	defer stop()
	clientA := newUDPClient(t)
	defer clientA.Close()
	clientB := newUDPClient(t)
	defer clientB.Close()

	sendUDP(t, clientA, relay.Addr(), []byte("a"))
	packetA := backend.read(t)
	sendUDP(t, clientB, relay.Addr(), []byte("b"))
	_ = backend.read(t)
	closeBackendSocketForClient(t, relay, clientA)
	waitRelaySessionCount(t, relay, 1)

	sendUDP(t, clientB, relay.Addr(), []byte("b2"))
	packetB2 := backend.read(t)
	backend.reply(t, packetB2.from, []byte("ok"))
	if got := readUDP(t, clientB); !bytes.Equal(got, []byte("ok")) {
		t.Fatalf("client B reply = %q, want ok", got)
	}
	backend.reply(t, packetA.from, []byte("stale"))
	assertNoClientPacket(t, clientA)
}

func TestRelayBackendWriteFailureIsIsolated(t *testing.T) {
	backend := newUDPBackend(t)
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	defer stop()
	clientA := newUDPClient(t)
	defer clientA.Close()
	clientB := newUDPClient(t)
	defer clientB.Close()

	sendUDP(t, clientA, relay.Addr(), []byte("a"))
	_ = backend.read(t)
	sendUDP(t, clientB, relay.Addr(), []byte("b"))
	_ = backend.read(t)
	failBackendWritesForClient(t, relay, clientA)
	sendUDP(t, clientA, relay.Addr(), []byte("fails"))
	waitRelaySessionCount(t, relay, 1)
	sendUDP(t, clientB, relay.Addr(), []byte("continues"))
	got := backend.read(t)
	if !bytes.Equal(got.payload, []byte("continues")) {
		t.Fatalf("backend payload = %q, want continues", got.payload)
	}
}

func TestRelayShutdownCancellationAndIdempotentClose(t *testing.T) {
	backend := newUDPBackend(t)
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	client := newUDPClient(t)
	defer client.Close()
	sendUDP(t, client, relay.Addr(), []byte("payload"))
	_ = backend.read(t)

	stop()
	if err := relay.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
	waitRelaySessionCount(t, relay, 0)
}

func TestRelayHandlesArbitraryAndMaxSizedDatagrams(t *testing.T) {
	backend := newUDPBackend(t)
	cfg := testConfig(backend.addr())
	cfg.MaxPacketSize = 32
	relay, _, stop := startTestRelayWithConfig(t, cfg, nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	payload := bytes.Repeat([]byte{0x80, 0x00, 0xff, 0x42}, 8)
	sendUDP(t, client, relay.Addr(), payload)
	got := backend.read(t)
	if !bytes.Equal(got.payload, payload) {
		t.Fatalf("backend payload changed: %v != %v", got.payload, payload)
	}
}

func TestRelayForwardsZeroLengthDatagram(t *testing.T) {
	backend := newUDPBackend(t)
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), nil)
	got := backend.read(t)
	if len(got.payload) != 0 {
		t.Fatalf("backend payload length = %d, want 0", len(got.payload))
	}
	backend.reply(t, got.from, nil)
	if reply := readUDP(t, client); len(reply) != 0 {
		t.Fatalf("client reply length = %d, want 0", len(reply))
	}
}

func TestRelayDropsOversizedDatagramWithoutForwardingTruncatedBytes(t *testing.T) {
	backend := newUDPBackend(t)
	cfg := testConfig(backend.addr())
	cfg.MaxPacketSize = 4
	relay, _, stop := startTestRelayWithConfig(t, cfg, nil)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), []byte("12345"))
	assertNoBackendPacket(t, backend)
	sendUDP(t, client, relay.Addr(), []byte("1234"))
	if got := backend.read(t); !bytes.Equal(got.payload, []byte("1234")) {
		t.Fatalf("backend payload = %q, want 1234", got.payload)
	}
}

func TestRelayMetricsUseBoundedLabels(t *testing.T) {
	backend := newUDPBackend(t)
	recorder := gatewaymetrics.NewRecorder(true)
	relay, _, stop := startTestRelay(t, backend.addr(), recorder)
	defer stop()
	client := newUDPClient(t)
	defer client.Close()

	sendUDP(t, client, relay.Addr(), []byte("hello"))
	packet := backend.read(t)
	backend.reply(t, packet.from, []byte("reply"))
	_ = readUDP(t, client)

	for _, name := range []string{
		"mc_gateway_udp_packets_total",
		"mc_gateway_udp_bytes_total",
		"mc_gateway_udp_sessions",
		"mc_gateway_udp_sessions_created_total",
	} {
		if metricFamily(t, recorder, name) == nil {
			t.Fatalf("metric %s was not exported", name)
		}
	}
	family := metricFamily(t, recorder, "mc_gateway_udp_packets_total")
	for _, metric := range family.GetMetric() {
		for _, label := range metric.GetLabel() {
			switch label.GetName() {
			case "direction", "result":
			default:
				t.Fatalf("unexpected UDP packet metric label %q", label.GetName())
			}
			if label.GetValue() == client.LocalAddr().String() || label.GetValue() == backend.addr() {
				t.Fatalf("metric label contains endpoint value %q", label.GetValue())
			}
		}
	}
}

func TestConcurrentClients(t *testing.T) {
	backend := newUDPBackend(t)

	cfg := testConfig(backend.addr())
	cfg.IdleTimeout = 10 * time.Second

	relay, _, stop := startTestRelayWithConfig(t, cfg, nil)
	defer stop()

	const clients = 32

	clientConns := make([]*net.UDPConn, 0, clients)
	for i := 0; i < clients; i++ {
		client := newUDPClient(t)
		clientConns = append(clientConns, client)
	}
	defer func() {
		for _, client := range clientConns {
			_ = client.Close()
		}
	}()

	var wg sync.WaitGroup
	for _, client := range clientConns {
		wg.Add(1)
		go func(client *net.UDPConn) {
			defer wg.Done()
			sendUDP(t, client, relay.Addr(), []byte("x"))
		}(client)
	}

	for i := 0; i < clients; i++ {
		_ = backend.read(t)
	}

	wg.Wait()
	waitRelaySessionCount(t, relay, clients)
}

type backendPacket struct {
	payload []byte
	from    *net.UDPAddr
}

type udpBackend struct {
	conn *net.UDPConn
}

type rotatingResolver struct {
	backends []*net.UDPAddr
	mu       sync.Mutex
	calls    int
}

type payloadResolver struct {
	defaultBackend  *net.UDPAddr
	creativeBackend *net.UDPAddr
}

func newUDPBackend(t *testing.T) *udpBackend {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP backend: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &udpBackend{conn: conn}
}

func (b *udpBackend) addr() string {
	return b.conn.LocalAddr().String()
}

func (b *udpBackend) read(t *testing.T) backendPacket {
	t.Helper()
	buf := make([]byte, 65535)
	if err := b.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline backend: %v", err)
	}
	n, from, err := b.conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("backend read: %v", err)
	}
	return backendPacket{payload: append([]byte(nil), buf[:n]...), from: cloneUDPAddr(from)}
}

func (b *udpBackend) reply(t *testing.T, to *net.UDPAddr, payload []byte) {
	t.Helper()
	if _, err := b.conn.WriteToUDP(payload, to); err != nil {
		t.Fatalf("backend reply: %v", err)
	}
}

func startTestRelay(t *testing.T, backend string, recorder *gatewaymetrics.Recorder) (*Relay, chan error, func()) {
	t.Helper()
	return startTestRelayWithConfig(t, testConfig(backend), recorder)
}

func startTestRelayWithConfig(t *testing.T, cfg Config, recorder *gatewaymetrics.Recorder) (*Relay, chan error, func()) {
	t.Helper()
	relay, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), recorder)
	if err != nil {
		t.Fatalf("New relay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Serve(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = relay.Close()
		})
	}
	t.Cleanup(func() {
		stop()
		waitRelayDone(t, done)
	})
	return relay, done, stop
}

func testConfig(backend string) Config {
	return Config{
		Listen:             "127.0.0.1:0",
		Backend:            backend,
		IdleTimeout:        time.Second,
		BackendDialTimeout: time.Second,
		MaxSessions:        64,
		MaxPacketSize:      65535,
	}
}

func (r *rotatingResolver) ResolveBackend(context.Context, *net.UDPAddr, []byte) (BackendSelection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	backend := r.backends[r.calls%len(r.backends)]
	r.calls++
	return BackendSelection{Name: "rotating", Addr: cloneUDPAddr(backend)}, nil
}

func (r *rotatingResolver) DefaultBackend() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.backends) == 0 {
		return ""
	}
	return r.backends[0].String()
}

func (r *rotatingResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *payloadResolver) ResolveBackend(_ context.Context, _ *net.UDPAddr, payload []byte) (BackendSelection, error) {
	if bytes.Equal(payload, []byte("creative")) {
		return BackendSelection{Name: "creative", Addr: cloneUDPAddr(r.creativeBackend)}, nil
	}
	return BackendSelection{Name: "default", Addr: cloneUDPAddr(r.defaultBackend)}, nil
}

func (r *payloadResolver) DefaultBackend() string {
	return r.defaultBackend.String()
}

func newUDPClient(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP client: %v", err)
	}
	return conn
}

func sendUDP(t *testing.T, conn *net.UDPConn, address string, payload []byte) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		t.Fatalf("ResolveUDPAddr: %v", err)
	}
	if _, err := conn.WriteToUDP(payload, addr); err != nil {
		t.Fatalf("WriteToUDP: %v", err)
	}
}

func mustResolveUDPAddr(t *testing.T, address string) *net.UDPAddr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		t.Fatalf("ResolveUDPAddr: %v", err)
	}
	return addr
}

func readUDP(t *testing.T, conn *net.UDPConn) []byte {
	t.Helper()
	buf := make([]byte, 65535)
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline client: %v", err)
	}
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	return append([]byte(nil), buf[:n]...)
}

func assertNoBackendPacket(t *testing.T, backend *udpBackend) {
	t.Helper()
	if err := backend.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline backend: %v", err)
	}
	_, _, err := backend.conn.ReadFromUDP(make([]byte, 1))
	if err == nil {
		t.Fatal("backend received unexpected packet")
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("backend read error = %v, want timeout", err)
	}
}

func assertNoClientPacket(t *testing.T, client *net.UDPConn) {
	t.Helper()
	if err := client.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline client: %v", err)
	}
	_, _, err := client.ReadFromUDP(make([]byte, 1))
	if err == nil {
		t.Fatal("client received unexpected packet")
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("client read error = %v, want timeout", err)
	}
}

func relaySessionCount(relay *Relay) int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return len(relay.sessions)
}

func waitRelaySessionCount(t *testing.T, relay *Relay, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := relaySessionCount(relay); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions = %d, want %d", relaySessionCount(relay), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func closeBackendSocketForClient(t *testing.T, relay *Relay, client *net.UDPConn) {
	t.Helper()
	key := client.LocalAddr().String()
	relay.mu.Lock()
	sess := relay.sessions[key]
	relay.mu.Unlock()
	if sess == nil {
		t.Fatalf("session %s not found", key)
	}
	_ = sess.backend.Close()
}

func failBackendWritesForClient(t *testing.T, relay *Relay, client *net.UDPConn) {
	t.Helper()
	key := client.LocalAddr().String()
	relay.mu.Lock()
	sess := relay.sessions[key]
	relay.mu.Unlock()
	if sess == nil {
		t.Fatalf("session %s not found", key)
	}
	sess.mu.Lock()
	sess.write = func([]byte) (int, error) {
		return 0, errors.New("injected backend write failure")
	}
	sess.mu.Unlock()
}

func waitRelayDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("relay returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop")
	}
}

func metricFamily(t *testing.T, recorder *gatewaymetrics.Recorder, name string) *dto.MetricFamily {
	t.Helper()
	families, err := recorder.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	return nil
}
