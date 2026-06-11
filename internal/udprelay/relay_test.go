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
	relay, _, stop := startTestRelay(t, backend.addr(), nil)
	defer stop()
	const clients = 32
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := newUDPClient(t)
			defer client.Close()
			sendUDP(t, client, relay.Addr(), []byte("x"))
		}()
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
