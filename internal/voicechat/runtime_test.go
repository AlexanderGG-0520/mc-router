package voicechat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistrationAPILifecycleAndStaleLease(t *testing.T) {
	r := newTestRuntime(t, map[string]string{"hub": "127.0.0.1:24454", "fabric": "127.0.0.1:24455"})
	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")

	rr := r.registrationRequest(t, http.MethodPut, uuid.String(), "", "hub-token", `{"ownerId":"hub-1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
	leaseA := decodeLease(t, rr.Body.Bytes()).LeaseID

	rr = r.registrationRequest(t, http.MethodPost, uuid.String(), "refresh", "hub-token", `{"ownerId":"hub-1","leaseId":"`+leaseA+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", rr.Code, rr.Body.String())
	}

	rr = r.registrationRequest(t, http.MethodPut, uuid.String(), "", "fabric-token", `{"ownerId":"fabric-1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("replacement PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
	leaseB := decodeLease(t, rr.Body.Bytes()).LeaseID
	if leaseA == leaseB {
		t.Fatal("replacement reused lease")
	}

	rr = r.registrationRequest(t, http.MethodDelete, uuid.String(), "", "hub-token", `{"ownerId":"hub-1","leaseId":"`+leaseA+`"}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("stale delete status = %d body=%s", rr.Code, rr.Body.String())
	}
	reg, err := r.lookupRegistration(uuid)
	if err != nil {
		t.Fatalf("registration missing after stale delete: %v", err)
	}
	if reg.BackendID != "fabric" || reg.LeaseID != leaseB {
		t.Fatalf("registration = backend %q lease %q, want fabric %q", reg.BackendID, reg.LeaseID, leaseB)
	}

	rr = r.registrationRequest(t, http.MethodDelete, uuid.String(), "", "fabric-token", `{"ownerId":"fabric-1","leaseId":"`+leaseB+`"}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := r.lookupRegistration(uuid); err == nil {
		t.Fatal("registration still exists after matching delete")
	}
}

func TestRegistrationAPIRejectsInvalidRequests(t *testing.T) {
	r := newTestRuntime(t, map[string]string{"hub": "127.0.0.1:24454"})
	uuid := "00112233-4455-6677-8899-aabbccddeeff"

	tests := []struct {
		name   string
		method string
		uuid   string
		token  string
		body   string
		want   int
	}{
		{name: "invalid token", method: http.MethodPut, uuid: uuid, token: "bad-token", body: `{"ownerId":"hub-1"}`, want: http.StatusUnauthorized},
		{name: "malformed uuid", method: http.MethodPut, uuid: "not-a-uuid", token: "hub-token", body: `{"ownerId":"hub-1"}`, want: http.StatusNotFound},
		{name: "unknown field", method: http.MethodPut, uuid: uuid, token: "hub-token", body: `{"ownerId":"hub-1","token":"secret"}`, want: http.StatusBadRequest},
		{name: "empty owner", method: http.MethodPut, uuid: uuid, token: "hub-token", body: `{"ownerId":""}`, want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := r.registrationRequest(t, tt.method, tt.uuid, "", tt.token, tt.body)
			if rr.Code != tt.want {
				t.Fatalf("status = %d body=%s, want %d", rr.Code, rr.Body.String(), tt.want)
			}
		})
	}
}

func TestRegistrationLimit(t *testing.T) {
	r := newTestRuntime(t, map[string]string{"hub": "127.0.0.1:24454"})
	r.cfg.MaxRegistrations = 1

	rr := r.registrationRequest(t, http.MethodPut, "00112233-4455-6677-8899-aabbccddeeff", "", "hub-token", `{"ownerId":"hub-1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("first PUT status = %d", rr.Code)
	}
	rr = r.registrationRequest(t, http.MethodPut, "11112233-4455-6677-8899-aabbccddeeff", "", "hub-token", `{"ownerId":"hub-1"}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRegistrationExpiration(t *testing.T) {
	r := newTestRuntime(t, map[string]string{"hub": "127.0.0.1:24454"})
	r.cfg.RegistrationTTL = time.Millisecond
	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")

	rr := r.registrationRequest(t, http.MethodPut, uuid.String(), "", "hub-token", `{"ownerId":"hub-1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", rr.Code)
	}
	reg, err := r.lookupRegistration(uuid)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	reg.ExpiresAt = time.Now().Add(-time.Second)
	r.registrations[uuid] = reg
	if _, err := r.lookupRegistration(uuid); err == nil {
		t.Fatal("expired registration lookup succeeded")
	}
}

func TestDynamicUDPRoutingAndReassignment(t *testing.T) {
	hub := newUDPBackend(t)
	fabric := newUDPBackend(t)
	r := newTestRuntime(t, map[string]string{"hub": hub.addr, "fabric": fabric.addr})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = r.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runtime Serve returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("runtime did not stop")
		}
	})

	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	registerForTest(t, r, uuid, "hub-token", "hub-1")
	client := newUDPClient(t)
	packet := clientPacket(t, uuid, []byte{1, 2, 3})
	if _, err := client.WriteToUDP(packet, mustResolveUDPAddr(t, r.UDPAddr())); err != nil {
		t.Fatalf("write to router: %v", err)
	}
	hubPayload, hubReply := hub.read(t)
	if !bytes.Equal(hubPayload, packet) {
		t.Fatalf("hub payload = %x, want %x", hubPayload, packet)
	}
	if _, err := hub.conn.WriteToUDP([]byte{0xff, 0x01}, hubReply); err != nil {
		t.Fatalf("hub reply: %v", err)
	}
	readUDPFrom(t, client, []byte{0xff, 0x01})

	registerForTest(t, r, uuid, "fabric-token", "fabric-1")
	if _, err := hub.conn.WriteToUDP([]byte{0xff, 0x02}, hubReply); err != nil {
		t.Fatalf("late hub reply: %v", err)
	}
	expectNoUDP(t, client)

	if _, err := client.WriteToUDP(packet, mustResolveUDPAddr(t, r.UDPAddr())); err != nil {
		t.Fatalf("write to router after reassignment: %v", err)
	}
	fabricPayload, _ := fabric.read(t)
	if !bytes.Equal(fabricPayload, packet) {
		t.Fatalf("fabric payload = %x, want %x", fabricPayload, packet)
	}
}

func TestUnknownAndMalformedUDPPacketsDrop(t *testing.T) {
	backend := newUDPBackend(t)
	r := newTestRuntime(t, map[string]string{"hub": backend.addr})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = r.Close()
		<-done
	})

	client := newUDPClient(t)
	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	routerAddr := mustResolveUDPAddr(t, r.UDPAddr())
	if _, err := client.WriteToUDP(clientPacket(t, uuid, []byte{1}), routerAddr); err != nil {
		t.Fatalf("unknown write: %v", err)
	}
	if _, err := client.WriteToUDP([]byte{0x00, 0x01}, routerAddr); err != nil {
		t.Fatalf("malformed write: %v", err)
	}
	backend.expectNoPacket(t)
}

func TestSameIPClientsRemainIsolated(t *testing.T) {
	hub := newUDPBackend(t)
	fabric := newUDPBackend(t)
	r := newTestRuntime(t, map[string]string{"hub": hub.addr, "fabric": fabric.addr})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = r.Close()
		<-done
	})

	uuidA := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	uuidB := mustParseUUID(t, "11112233-4455-6677-8899-aabbccddeeff")
	registerForTest(t, r, uuidA, "hub-token", "hub-1")
	registerForTest(t, r, uuidB, "fabric-token", "fabric-1")

	routerAddr := mustResolveUDPAddr(t, r.UDPAddr())
	clientA := newUDPClient(t)
	clientB := newUDPClient(t)
	packetA := clientPacket(t, uuidA, []byte{0xa})
	packetB := clientPacket(t, uuidB, []byte{0xb})
	if _, err := clientA.WriteToUDP(packetA, routerAddr); err != nil {
		t.Fatalf("client A write: %v", err)
	}
	if _, err := clientB.WriteToUDP(packetB, routerAddr); err != nil {
		t.Fatalf("client B write: %v", err)
	}
	gotA, _ := hub.read(t)
	gotB, _ := fabric.read(t)
	if !bytes.Equal(gotA, packetA) {
		t.Fatalf("hub got %x, want %x", gotA, packetA)
	}
	if !bytes.Equal(gotB, packetB) {
		t.Fatalf("fabric got %x, want %x", gotB, packetB)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	r := newTestRuntime(t, map[string]string{"hub": "127.0.0.1:24454"})
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

type testBackend struct {
	conn *net.UDPConn
	addr string
}

func newTestRuntime(t *testing.T, backendAddrs map[string]string) *Runtime {
	t.Helper()
	backends := map[string]BackendConfig{}
	for id, addr := range backendAddrs {
		backends[id] = BackendConfig{UDP: addr, Token: id + "-token"}
	}
	r, err := NewRuntime(Config{
		Listen:             "127.0.0.1:0",
		RegistrationListen: "127.0.0.1:0",
		RegistrationTTL:    time.Minute,
		RequestTimeout:     time.Second,
		MaxRegistrations:   32,
		IdleTimeout:        time.Minute,
		BackendDialTimeout: time.Second,
		MaxSessions:        32,
		MaxPacketSize:      65535,
		Backends:           backends,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func (r *Runtime) registrationRequest(t *testing.T, method, uuid, action, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/v1/voicechat/registrations/" + uuid
	if action != "" {
		path += "/" + action
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.handleRegistration(rr, req)
	return rr
}

func registerForTest(t *testing.T, r *Runtime, uuid UUID, token string, owner string) string {
	t.Helper()
	rr := r.registrationRequest(t, http.MethodPut, uuid.String(), "", token, `{"ownerId":"`+owner+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("register status = %d body=%s", rr.Code, rr.Body.String())
	}
	return decodeLease(t, rr.Body.Bytes()).LeaseID
}

func decodeLease(t *testing.T, body []byte) registrationResponse {
	t.Helper()
	var resp registrationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response %q: %v", string(body), err)
	}
	return resp
}

func newUDPBackend(t *testing.T) *testBackend {
	t.Helper()
	conn, err := net.ListenUDP("udp", mustResolveUDPAddr(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &testBackend{conn: conn, addr: conn.LocalAddr().String()}
}

func (b *testBackend) read(t *testing.T) ([]byte, *net.UDPAddr) {
	t.Helper()
	if err := b.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 65535)
	n, addr, err := b.conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("backend read: %v", err)
	}
	return append([]byte(nil), buf[:n]...), addr
}

func (b *testBackend) expectNoPacket(t *testing.T) {
	t.Helper()
	if err := b.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1024)
	if n, _, err := b.conn.ReadFromUDP(buf); err == nil {
		t.Fatalf("unexpected backend packet: %x", buf[:n])
	}
}

func newUDPClient(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", mustResolveUDPAddr(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatalf("ListenUDP client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readUDPFrom(t *testing.T, conn *net.UDPConn, want []byte) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("client got %x, want %x", buf[:n], want)
	}
}

func expectNoUDP(t *testing.T, conn *net.UDPConn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1024)
	if n, _, err := conn.ReadFromUDP(buf); err == nil {
		t.Fatalf("unexpected client packet: %x", buf[:n])
	}
}

func clientPacket(t *testing.T, uuid UUID, payload []byte) []byte {
	t.Helper()
	packet := append([]byte{MagicByte}, uuid[:]...)
	packet = appendVarInt(packet, len(payload))
	packet = append(packet, payload...)
	return packet
}

func appendVarInt(dst []byte, value int) []byte {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			b |= 0x80
		}
		dst = append(dst, b)
		if value == 0 {
			return dst
		}
	}
}

func mustResolveUDPAddr(t *testing.T, addr string) *net.UDPAddr {
	t.Helper()
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("ResolveUDPAddr(%q): %v", addr, err)
	}
	return udpAddr
}
