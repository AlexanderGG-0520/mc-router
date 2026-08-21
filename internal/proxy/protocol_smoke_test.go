package proxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
)

const smokeProtocolVersion int32 = 767

func TestProtocolSmokeStatusFlow(t *testing.T) {
	statusBackend := startStatusProtocolBackend(t)
	defer statusBackend.close()

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Status:             config.Status{RecoveryThreshold: 1},
		Routes: []config.Route{
			{ServerAddress: "status.example.com", Backend: statusBackend.addr},
		},
	})
	defer stop()
	triggerObservedStatus(t, gatewayAddr, "STATUS.Example.COM.")
	result := waitProtocolResult(t, statusBackend.result)

	client := dialProtocolClient(t, gatewayAddr)
	defer client.Close()
	clientReader := bufio.NewReader(client)
	requestedAddress := "STATUS.Example.COM."
	pingPayload := int64(0x1122334455667788)
	writeProtocolBytes(t, client,
		buildHandshakePacket(smokeProtocolVersion, requestedAddress, 25565, mcproto.NextStateStatus),
		buildPacket(0x00),
	)

	statusID, statusPayload := readProtocolPacket(t, clientReader)
	if statusID != 0x00 {
		t.Fatalf("status response packet id = %d, want 0", statusID)
	}
	statusJSON := readProtocolString(t, statusPayload)
	var status map[string]any
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("status response is not JSON: %v", err)
	}
	if version, ok := status["version"].(map[string]any); !ok || version["name"] != "mc-router-smoke" {
		t.Fatalf("unexpected status response: %s", statusJSON)
	}
	if status["favicon"] != "data:image/png;base64,AA==" || status["enforcesSecureChat"] != true {
		t.Fatalf("status response was not passed through: %s", statusJSON)
	}
	if players, ok := status["players"].(map[string]any); !ok || len(players["sample"].([]any)) != 1 {
		t.Fatalf("status player sample was not passed through: %s", statusJSON)
	}

	writeProtocolBytes(t, client, buildPacket(0x01, encodeLong(pingPayload)))
	pongID, pongPayload := readProtocolPacket(t, clientReader)
	if pongID != 0x01 {
		t.Fatalf("pong packet id = %d, want 1", pongID)
	}
	if got := readLong(t, pongPayload); got != pingPayload {
		t.Fatalf("pong payload = %x, want %x", got, pingPayload)
	}

	if result.handshake.ServerAddress != "status.example.com" {
		t.Fatalf("backend handshake server address = %q, want canonical route address", result.handshake.ServerAddress)
	}
	if result.handshake.RouteAddress() != "status.example.com" {
		t.Fatalf("backend route address = %q", result.handshake.RouteAddress())
	}
	if result.handshake.NextState != mcproto.NextStateStatus {
		t.Fatalf("backend next state = %d, want status", result.handshake.NextState)
	}
}

func TestProtocolSmokeStatusBackendFlow(t *testing.T) {
	statusBackend := startStatusProtocolBackend(t)
	defer statusBackend.close()

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Status:             config.Status{RecoveryThreshold: 1},
		Routes: []config.Route{
			{ServerAddress: "status.example.com", Backend: "127.0.0.1:1", StatusBackend: statusBackend.addr},
		},
	})
	defer stop()
	triggerObservedStatus(t, gatewayAddr, "STATUS.Example.COM.")
	result := waitProtocolResult(t, statusBackend.result)

	client := dialProtocolClient(t, gatewayAddr)
	defer client.Close()
	clientReader := bufio.NewReader(client)
	requestedAddress := "STATUS.Example.COM."
	pingPayload := int64(0x1122334455667788)
	writeProtocolBytes(t, client,
		buildHandshakePacket(smokeProtocolVersion, requestedAddress, 25565, mcproto.NextStateStatus),
		buildPacket(0x00),
	)

	statusID, statusPayload := readProtocolPacket(t, clientReader)
	if statusID != 0x00 {
		t.Fatalf("status response packet id = %d, want 0", statusID)
	}
	statusJSON := readProtocolString(t, statusPayload)
	var status map[string]any
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("status response is not JSON: %v", err)
	}
	if version, ok := status["version"].(map[string]any); !ok || version["name"] != "mc-router-smoke" {
		t.Fatalf("unexpected status response: %s", statusJSON)
	}
	if status["favicon"] != "data:image/png;base64,AA==" || status["enforcesSecureChat"] != true {
		t.Fatalf("status response was not passed through: %s", statusJSON)
	}
	if players, ok := status["players"].(map[string]any); !ok || len(players["sample"].([]any)) != 1 {
		t.Fatalf("status player sample was not passed through: %s", statusJSON)
	}

	writeProtocolBytes(t, client, buildPacket(0x01, encodeLong(pingPayload)))
	pongID, pongPayload := readProtocolPacket(t, clientReader)
	if pongID != 0x01 {
		t.Fatalf("pong packet id = %d, want 1", pongID)
	}
	if got := readLong(t, pongPayload); got != pingPayload {
		t.Fatalf("pong payload = %x, want %x", got, pingPayload)
	}

	if result.handshake.ServerAddress != "status.example.com" || result.handshake.NextState != mcproto.NextStateStatus {
		t.Fatalf("status backend handshake = %#v", result.handshake)
	}
}

func TestProtocolSmokeLoginStartFlow(t *testing.T) {
	loginBackend := startLoginProtocolBackend(t)
	defer loginBackend.close()

	gatewayAddr, stop := startTestServer(t, config.Config{
		Listen:             ":0",
		HandshakeTimeout:   config.Duration{Duration: time.Second},
		BackendDialTimeout: config.Duration{Duration: time.Second},
		UnknownHostPolicy:  config.UnknownHostDeny,
		Routes: []config.Route{
			{ServerAddress: "login.example.com", Backend: loginBackend.addr, StatusBackend: "127.0.0.1:1"},
		},
	})
	defer stop()

	client := dialProtocolClient(t, gatewayAddr)
	defer client.Close()
	clientReader := bufio.NewReader(client)
	requestedAddress := "LOGIN.Example.COM."
	username := "SmokeUser"
	writeProtocolBytes(t, client,
		buildHandshakePacket(smokeProtocolVersion, requestedAddress, 25565, mcproto.NextStateLogin),
		buildLoginStartPacket(username),
	)

	disconnectID, disconnectPayload := readProtocolPacket(t, clientReader)
	if disconnectID != 0x00 {
		t.Fatalf("login disconnect packet id = %d, want 0", disconnectID)
	}
	reason := readProtocolString(t, disconnectPayload)
	if !bytes.Contains([]byte(reason), []byte("mc-router smoke disconnect")) {
		t.Fatalf("unexpected disconnect reason: %s", reason)
	}

	result := waitProtocolResult(t, loginBackend.result)
	if result.handshake.ServerAddress != requestedAddress {
		t.Fatalf("backend handshake server address = %q, want %q", result.handshake.ServerAddress, requestedAddress)
	}
	if result.handshake.RouteAddress() != "login.example.com" {
		t.Fatalf("backend route address = %q", result.handshake.RouteAddress())
	}
	if result.handshake.NextState != mcproto.NextStateLogin {
		t.Fatalf("backend next state = %d, want login", result.handshake.NextState)
	}
	if result.loginName != username {
		t.Fatalf("backend login name = %q, want %q", result.loginName, username)
	}
}

type protocolBackend struct {
	addr     string
	close    func()
	result   <-chan protocolResult
	listener net.Listener
}

type protocolResult struct {
	handshake   mcproto.Handshake
	pingPayload int64
	loginName   string
	err         error
}

func startStatusProtocolBackend(t *testing.T) protocolBackend {
	t.Helper()
	listener := listenLocalTCP(t)
	result := make(chan protocolResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- protocolResult{err: fmt.Errorf("accept status backend: %w", err)}
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			result <- protocolResult{err: fmt.Errorf("set status backend deadline: %w", err)}
			return
		}
		br := bufio.NewReader(conn)
		handshake, _, err := mcproto.ReadHandshake(br, mcproto.DefaultLimits())
		if err != nil {
			result <- protocolResult{err: fmt.Errorf("read status handshake: %w", err)}
			return
		}
		packetID, payload, err := readFramedPacket(br)
		if err != nil {
			result <- protocolResult{err: fmt.Errorf("read status request: %w", err)}
			return
		}
		if packetID != 0x00 || len(payload) != 0 {
			result <- protocolResult{err: fmt.Errorf("invalid status request packet id=%d payload=%v", packetID, payload)}
			return
		}
		statusJSON := `{"version":{"name":"mc-router-smoke","protocol":767},"players":{"max":20,"online":0,"sample":[{"name":"SamplePlayer","id":"00000000-0000-0000-0000-000000000000"}]},"description":{"text":"mc-router smoke"},"favicon":"data:image/png;base64,AA==","enforcesSecureChat":true}`
		if err := writeProtocolPacket(conn, 0x00, encodeString(statusJSON)); err != nil {
			result <- protocolResult{err: fmt.Errorf("write status response: %w", err)}
			return
		}
		result <- protocolResult{handshake: handshake}
	}()
	return protocolBackend{
		addr:     listener.Addr().String(),
		close:    func() { _ = listener.Close() },
		result:   result,
		listener: listener,
	}
}

func triggerObservedStatus(t *testing.T, gatewayAddr, address string) {
	t.Helper()
	client := dialProtocolClient(t, gatewayAddr)
	defer client.Close()
	writeProtocolBytes(t, client,
		buildHandshakePacket(smokeProtocolVersion, address, 25565, mcproto.NextStateStatus),
		buildPacket(mcproto.StatusRequestPacketID),
	)
	_ = readStatusResponse(t, client)
}

func startLoginProtocolBackend(t *testing.T) protocolBackend {
	t.Helper()
	listener := listenLocalTCP(t)
	result := make(chan protocolResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- protocolResult{err: fmt.Errorf("accept login backend: %w", err)}
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			result <- protocolResult{err: fmt.Errorf("set login backend deadline: %w", err)}
			return
		}
		br := bufio.NewReader(conn)
		handshake, _, err := mcproto.ReadHandshake(br, mcproto.DefaultLimits())
		if err != nil {
			result <- protocolResult{err: fmt.Errorf("read login handshake: %w", err)}
			return
		}
		packetID, payload, err := readFramedPacket(br)
		if err != nil {
			result <- protocolResult{err: fmt.Errorf("read login start: %w", err)}
			return
		}
		if packetID != 0x00 {
			result <- protocolResult{err: fmt.Errorf("invalid login start packet id=%d", packetID)}
			return
		}
		name, remaining, err := parseString(payload)
		if err != nil {
			result <- protocolResult{err: fmt.Errorf("parse login name: %w", err)}
			return
		}
		if len(remaining) != 16 {
			result <- protocolResult{err: fmt.Errorf("login start uuid bytes = %d, want 16", len(remaining))}
			return
		}
		disconnect := `{"text":"mc-router smoke disconnect"}`
		if err := writeProtocolPacket(conn, 0x00, encodeString(disconnect)); err != nil {
			result <- protocolResult{err: fmt.Errorf("write login disconnect: %w", err)}
			return
		}
		result <- protocolResult{handshake: handshake, loginName: name}
	}()
	return protocolBackend{
		addr:     listener.Addr().String(),
		close:    func() { _ = listener.Close() },
		result:   result,
		listener: listener,
	}
}

func waitProtocolResult(t *testing.T, ch <-chan protocolResult) protocolResult {
	t.Helper()
	select {
	case result := <-ch:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for protocol backend result")
		return protocolResult{}
	}
}

func dialProtocolClient(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial protocol client: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		_ = conn.Close()
		t.Fatalf("set protocol client deadline: %v", err)
	}
	return conn
}

func writeProtocolBytes(t *testing.T, w io.Writer, chunks ...[]byte) {
	t.Helper()
	for _, chunk := range chunks {
		if err := writeAllWriter(w, chunk); err != nil {
			t.Fatalf("write protocol bytes: %v", err)
		}
	}
}

func readProtocolPacket(t *testing.T, r *bufio.Reader) (int32, []byte) {
	t.Helper()
	packetID, payload, err := readFramedPacket(r)
	if err != nil {
		t.Fatalf("read protocol packet: %v", err)
	}
	return packetID, payload
}

func readFramedPacket(r *bufio.Reader) (int32, []byte, error) {
	length, _, err := mcproto.ReadVarInt(r)
	if err != nil {
		return 0, nil, fmt.Errorf("read packet length: %w", err)
	}
	if length <= 0 || length > 4096 {
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

func buildPacket(packetID int32, fields ...[]byte) []byte {
	var payload []byte
	payload = append(payload, mcproto.WriteVarInt(packetID)...)
	for _, field := range fields {
		payload = append(payload, field...)
	}
	return append(mcproto.WriteVarInt(int32(len(payload))), payload...)
}

func buildLoginStartPacket(username string) []byte {
	return buildPacket(0x00, encodeString(username), make([]byte, 16))
}

func writeProtocolPacket(w io.Writer, packetID int32, fields ...[]byte) error {
	return writeAllWriter(w, buildPacket(packetID, fields...))
}

func encodeString(value string) []byte {
	raw := []byte(value)
	out := mcproto.WriteVarInt(int32(len(raw)))
	out = append(out, raw...)
	return out
}

func readProtocolString(t *testing.T, payload []byte) string {
	t.Helper()
	value, _, err := parseString(payload)
	if err != nil {
		t.Fatalf("read string: %v", err)
	}
	return value
}

func encodeLong(value int64) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], uint64(value))
	return out[:]
}

func readLong(t *testing.T, payload []byte) int64 {
	t.Helper()
	value, err := parseLong(payload)
	if err != nil {
		t.Fatal(err)
	}
	return value
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

func parseLong(payload []byte) (int64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("long payload length = %d, want 8", len(payload))
	}
	return int64(binary.BigEndian.Uint64(payload)), nil
}

func writeAllWriter(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
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
