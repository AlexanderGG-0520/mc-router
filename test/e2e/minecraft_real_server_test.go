//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
	"github.com/AlexanderGG-0520/mc-router/internal/router"
)

const (
	e2eRouteAddress           = "e2e.example.com"
	defaultE2EProtocolVersion = int32(767) // Minecraft Java Edition 1.21/1.21.1.
)

func TestRealMinecraftStatusThroughGateway(t *testing.T) {
	backend := requiredEnv(t, "MC_ROUTER_E2E_BACKEND")
	protocolVersion := envInt32(t, "MC_ROUTER_E2E_PROTOCOL_VERSION", defaultE2EProtocolVersion)
	timeout := envDuration(t, "MC_ROUTER_E2E_TIMEOUT", 4*time.Minute)

	gatewayAddr, stop := startGateway(t, backend)
	defer stop()

	status, pongPayload := waitForStatusFlow(t, gatewayAddr, protocolVersion, timeout)
	if status.Version.Name == "" {
		t.Fatalf("status response version name is empty: %+v", status)
	}
	if status.Version.Protocol == 0 {
		t.Fatalf("status response protocol is empty: %+v", status)
	}
	if pongPayload != statusPingPayload {
		t.Fatalf("pong payload = %x, want %x", pongPayload, statusPingPayload)
	}
}

func TestRealMinecraftLoginStartThroughGateway(t *testing.T) {
	backend := requiredEnv(t, "MC_ROUTER_E2E_BACKEND")
	protocolVersion := envInt32(t, "MC_ROUTER_E2E_PROTOCOL_VERSION", defaultE2EProtocolVersion)
	timeout := envDuration(t, "MC_ROUTER_E2E_TIMEOUT", 4*time.Minute)
	expect := strings.TrimSpace(os.Getenv("MC_ROUTER_E2E_LOGIN_EXPECT"))
	if expect == "" {
		expect = "any"
	}
	if expect == "skip" {
		t.Skip("MC_ROUTER_E2E_LOGIN_EXPECT=skip")
	}

	gatewayAddr, stop := startGateway(t, backend)
	defer stop()

	response := waitForLoginStartFlow(t, gatewayAddr, protocolVersion, timeout)
	t.Logf("login response: %s", describeLoginResponse(response))
	switch expect {
	case "any":
		if !isKnownLoginResponse(response.packetID) {
			t.Fatalf("login response packet id = 0x%02x, want a known login-state response; %s", response.packetID, describeLoginResponse(response))
		}
	case "encryption_request":
		if response.packetID != 0x01 {
			t.Fatalf("login response packet id = 0x%02x, want encryption request 0x01; %s", response.packetID, describeLoginResponse(response))
		}
		if err := validateEncryptionRequest(response.payload); err != nil {
			t.Fatalf("invalid encryption request payload: %v", err)
		}
	default:
		t.Fatalf("unsupported MC_ROUTER_E2E_LOGIN_EXPECT %q", expect)
	}
}

type statusResponse struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int    `json:"protocol"`
	} `json:"version"`
}

type loginResponse struct {
	packetID int32
	payload  []byte
}

const statusPingPayload int64 = 0x1122334455667788

func startGateway(t *testing.T, backend string) (string, func()) {
	t.Helper()
	if gatewayBin := strings.TrimSpace(os.Getenv("MC_ROUTER_E2E_GATEWAY_BIN")); gatewayBin != "" {
		return startGatewayProcess(t, gatewayBin, backend)
	}
	return startGatewayInProcess(t, backend)
}

func startGatewayInProcess(t *testing.T, backend string) (string, func()) {
	t.Helper()
	cfg := config.Config{
		Listen:             "127.0.0.1:0",
		HandshakeTimeout:   config.Duration{Duration: 5 * time.Second},
		BackendDialTimeout: config.Duration{Duration: 5 * time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: e2eRouteAddress, Backend: backend},
		},
	}
	routeTable, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("listen gateway: %v", err)
	}
	server := proxy.NewServer(cfg, routeTable, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			_ = listener.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("gateway serve returned error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("gateway did not stop")
			}
		})
	}
	return listener.Addr().String(), stop
}

func startGatewayProcess(t *testing.T, gatewayBin string, backend string) (string, func()) {
	t.Helper()
	listenAddr := freeLocalAddr(t)
	configPath := writeGatewayConfig(t, listenAddr, backend)

	cmd := exec.Command(gatewayBin, "-config", configPath, "-log-level", "debug")
	output := &lockedBuffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mc-gateway binary %q: %v", gatewayBin, err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs := strings.TrimSpace(output.String())
			if logs == "" {
				t.Log("mc-gateway produced no logs")
				return
			}
			t.Logf("mc-gateway logs:\n%s", logs)
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		t.Fatalf("mc-gateway exited during startup: %v\n%s", err, output.String())
	case <-time.After(150 * time.Millisecond):
	}

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("mc-gateway exited before test cleanup: %v\n%s", err, output.String())
				}
				return
			default:
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatalf("kill mc-gateway: %v\n%s", err, output.String())
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatalf("mc-gateway did not exit after kill\n%s", output.String())
			}
		})
	}
	return listenAddr, stop
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release local address: %v", err)
	}
	return addr
}

func writeGatewayConfig(t *testing.T, listenAddr string, backend string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mc-gateway-e2e.yaml")
	data := fmt.Sprintf(`listen: %s
handshakeTimeout: "5s"
backendDialTimeout: "5s"
unknownHostPolicy: "deny"
routes:
  - serverAddress: %s
    backend: %s
`, strconv.Quote(listenAddr), strconv.Quote(e2eRouteAddress), strconv.Quote(backend))
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}
	return path
}

func waitForStatusFlow(t *testing.T, gatewayAddr string, protocolVersion int32, timeout time.Duration) (statusResponse, int64) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		status, pong, err := runStatusFlow(gatewayAddr, protocolVersion)
		if err == nil {
			return status, pong
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("status flow did not succeed within %s after %d attempts; last error: %v", timeout, attempts, lastErr)
	return statusResponse{}, 0
}

func waitForLoginStartFlow(t *testing.T, gatewayAddr string, protocolVersion int32, timeout time.Duration) loginResponse {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		response, err := runLoginStartFlow(gatewayAddr, protocolVersion)
		if err == nil {
			return response
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("login start flow did not succeed within %s after %d attempts; last error: %v", timeout, attempts, lastErr)
	return loginResponse{}
}

func runStatusFlow(gatewayAddr string, protocolVersion int32) (statusResponse, int64, error) {
	conn, reader, err := dialGateway(gatewayAddr)
	if err != nil {
		return statusResponse{}, 0, err
	}
	defer conn.Close()

	if err := writeProtocolBytes(conn,
		buildHandshakePacket(protocolVersion, e2eRouteAddress, 25565, mcproto.NextStateStatus),
		buildPacket(0x00),
	); err != nil {
		return statusResponse{}, 0, err
	}

	packetID, payload, err := readFramedPacket(reader)
	if err != nil {
		return statusResponse{}, 0, fmt.Errorf("read status response: %w", err)
	}
	if packetID != 0x00 {
		return statusResponse{}, 0, fmt.Errorf("status response packet id = 0x%02x, want 0x00", packetID)
	}
	statusJSON, remaining, err := parseString(payload)
	if err != nil {
		return statusResponse{}, 0, fmt.Errorf("parse status json: %w", err)
	}
	if len(remaining) != 0 {
		return statusResponse{}, 0, fmt.Errorf("status response has %d trailing bytes", len(remaining))
	}
	var status statusResponse
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		return statusResponse{}, 0, fmt.Errorf("decode status json: %w", err)
	}

	if err := writeProtocolBytes(conn, buildPacket(0x01, encodeLong(statusPingPayload))); err != nil {
		return statusResponse{}, 0, err
	}
	packetID, payload, err = readFramedPacket(reader)
	if err != nil {
		return statusResponse{}, 0, fmt.Errorf("read pong response: %w", err)
	}
	if packetID != 0x01 {
		return statusResponse{}, 0, fmt.Errorf("pong response packet id = 0x%02x, want 0x01", packetID)
	}
	pong, err := parseLong(payload)
	if err != nil {
		return statusResponse{}, 0, fmt.Errorf("parse pong payload: %w", err)
	}
	return status, pong, nil
}

func runLoginStartFlow(gatewayAddr string, protocolVersion int32) (loginResponse, error) {
	conn, reader, err := dialGateway(gatewayAddr)
	if err != nil {
		return loginResponse{}, err
	}
	defer conn.Close()

	if err := writeProtocolBytes(conn,
		buildHandshakePacket(protocolVersion, e2eRouteAddress, 25565, mcproto.NextStateLogin),
		buildLoginStartPacket("RouterE2E"),
	); err != nil {
		return loginResponse{}, err
	}
	packetID, payload, err := readFramedPacket(reader)
	if err != nil {
		return loginResponse{}, fmt.Errorf("read login response: %w", err)
	}
	return loginResponse{packetID: packetID, payload: payload}, nil
}

func dialGateway(gatewayAddr string) (net.Conn, *bufio.Reader, error) {
	conn, err := net.DialTimeout("tcp", gatewayAddr, 3*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("dial gateway: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("set gateway deadline: %w", err)
	}
	return conn, bufio.NewReader(conn), nil
}

func isKnownLoginResponse(packetID int32) bool {
	switch packetID {
	case 0x00, 0x01, 0x02, 0x03, 0x04, 0x05:
		return true
	default:
		return false
	}
}

func describeLoginResponse(response loginResponse) string {
	name := map[int32]string{
		0x00: "disconnect",
		0x01: "encryption_request",
		0x02: "login_success",
		0x03: "set_compression",
		0x04: "login_plugin_request",
		0x05: "cookie_request",
	}[response.packetID]
	if name == "" {
		name = "unknown"
	}
	description := fmt.Sprintf("packet_id=0x%02x (%s), payload_bytes=%d", response.packetID, name, len(response.payload))
	if response.packetID == 0x00 {
		reason, _, err := parseString(response.payload)
		if err == nil {
			description += ", disconnect_reason=" + reason
		}
	}
	return description
}

func validateEncryptionRequest(payload []byte) error {
	serverID, remaining, err := parseString(payload)
	if err != nil {
		return fmt.Errorf("server id: %w", err)
	}
	if serverID != "" {
		return fmt.Errorf("server id = %q, want empty string for modern online-mode servers", serverID)
	}
	publicKey, remaining, err := parseByteArray(remaining)
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	if len(publicKey) == 0 {
		return errors.New("public key is empty")
	}
	verifyToken, remaining, err := parseByteArray(remaining)
	if err != nil {
		return fmt.Errorf("verify token: %w", err)
	}
	if len(verifyToken) == 0 {
		return errors.New("verify token is empty")
	}
	if len(remaining) == 0 {
		return nil
	}
	shouldAuthenticate, remaining, err := parseBool(remaining)
	if err != nil {
		return fmt.Errorf("should authenticate: %w", err)
	}
	if !shouldAuthenticate {
		return errors.New("should authenticate is false")
	}
	if len(remaining) != 0 {
		return fmt.Errorf("encryption request has %d trailing bytes", len(remaining))
	}
	return nil
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func readFramedPacket(r *bufio.Reader) (int32, []byte, error) {
	length, _, err := mcproto.ReadVarInt(r)
	if err != nil {
		return 0, nil, fmt.Errorf("read packet length: %w", err)
	}
	if length <= 0 || length > 2*1024*1024 {
		return 0, nil, fmt.Errorf("invalid packet length %d", length)
	}
	packet := make([]byte, length)
	if _, err := io.ReadFull(r, packet); err != nil {
		return 0, nil, fmt.Errorf("read packet payload: %w", err)
	}
	packetReader := bytes.NewReader(packet)
	packetID, _, err := mcproto.ReadVarInt(packetReader)
	if err != nil {
		return 0, nil, fmt.Errorf("read packet id: %w", err)
	}
	payload, err := io.ReadAll(packetReader)
	if err != nil {
		return 0, nil, fmt.Errorf("read packet data: %w", err)
	}
	return packetID, payload, nil
}

func buildHandshakePacket(protocol int32, address string, port uint16, nextState int32) []byte {
	var payload []byte
	payload = append(payload, mcproto.WriteVarInt(mcproto.HandshakePacketID)...)
	payload = append(payload, mcproto.WriteVarInt(protocol)...)
	payload = append(payload, encodeString(address)...)
	var portRaw [2]byte
	binary.BigEndian.PutUint16(portRaw[:], port)
	payload = append(payload, portRaw[:]...)
	payload = append(payload, mcproto.WriteVarInt(nextState)...)
	return append(mcproto.WriteVarInt(int32(len(payload))), payload...)
}

func buildLoginStartPacket(username string) []byte {
	return buildPacket(0x00, encodeString(username), make([]byte, 16))
}

func buildPacket(packetID int32, fields ...[]byte) []byte {
	var payload []byte
	payload = append(payload, mcproto.WriteVarInt(packetID)...)
	for _, field := range fields {
		payload = append(payload, field...)
	}
	return append(mcproto.WriteVarInt(int32(len(payload))), payload...)
}

func encodeString(value string) []byte {
	raw := []byte(value)
	out := mcproto.WriteVarInt(int32(len(raw)))
	out = append(out, raw...)
	return out
}

func encodeLong(value int64) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], uint64(value))
	return out[:]
}

func parseString(payload []byte) (string, []byte, error) {
	reader := bytes.NewReader(payload)
	length, _, err := mcproto.ReadVarInt(reader)
	if err != nil {
		return "", nil, fmt.Errorf("read string length: %w", err)
	}
	if length < 0 || length > int32(reader.Len()) {
		return "", nil, fmt.Errorf("invalid string length %d with %d bytes remaining", length, reader.Len())
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", nil, fmt.Errorf("read string bytes: %w", err)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil {
		return "", nil, fmt.Errorf("read remaining string payload: %w", err)
	}
	return string(raw), remaining, nil
}

func parseByteArray(payload []byte) ([]byte, []byte, error) {
	reader := bytes.NewReader(payload)
	length, _, err := mcproto.ReadVarInt(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("read byte array length: %w", err)
	}
	if length < 0 || length > int32(reader.Len()) {
		return nil, nil, fmt.Errorf("invalid byte array length %d with %d bytes remaining", length, reader.Len())
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return nil, nil, fmt.Errorf("read byte array: %w", err)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("read remaining byte array payload: %w", err)
	}
	return raw, remaining, nil
}

func parseBool(payload []byte) (bool, []byte, error) {
	if len(payload) == 0 {
		return false, nil, errors.New("missing bool")
	}
	switch payload[0] {
	case 0:
		return false, payload[1:], nil
	case 1:
		return true, payload[1:], nil
	default:
		return false, nil, fmt.Errorf("invalid bool byte %d", payload[0])
	}
}

func parseLong(payload []byte) (int64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("long payload length = %d, want 8", len(payload))
	}
	return int64(binary.BigEndian.Uint64(payload)), nil
}

func writeProtocolBytes(w io.Writer, chunks ...[]byte) error {
	for _, chunk := range chunks {
		for len(chunk) > 0 {
			n, err := w.Write(chunk)
			if err != nil {
				return fmt.Errorf("write protocol bytes: %w", err)
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			chunk = chunk[n:]
		}
	}
	return nil
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("%s is required; set it to the real Minecraft server host:port", key)
	}
	return value
}

func envInt32(t *testing.T, key string, fallback int32) int32 {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", key, value, err)
	}
	return int32(parsed)
}

func envDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", key, value, err)
	}
	return parsed
}
