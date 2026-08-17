// Package availability probes configured Java Edition backends and reports state changes.
package availability

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/mcproto"
)

const statusProtocolVersion = 767

// Start begins monitoring in the background. A missing token is a startup error.
func Start(ctx context.Context, cfg config.Availability, logger *slog.Logger) error {
	if !cfg.Enabled {
		return nil
	}
	token := strings.TrimSpace(os.Getenv(cfg.TokenEnv))
	if token == "" {
		return fmt.Errorf("availability token environment variable %q is empty", cfg.TokenEnv)
	}
	m := monitor{cfg: cfg, token: token, client: &http.Client{Timeout: cfg.Timeout.Duration}, sent: make(map[string]bool), logger: logger}
	go m.run(ctx)
	return nil
}

type monitor struct {
	cfg    config.Availability
	token  string
	client *http.Client
	sent   map[string]bool
	logger *slog.Logger
}

func (m *monitor) run(ctx context.Context) {
	m.checkAll(ctx)
	ticker := time.NewTicker(m.cfg.Interval.Duration)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

func (m *monitor) checkAll(ctx context.Context) {
	for _, backend := range m.cfg.Backends {
		probeCtx, cancel := context.WithTimeout(ctx, m.cfg.Timeout.Duration)
		online := Probe(probeCtx, backend.Address, backend.ServerAddress) == nil
		cancel()
		previous, known := m.sent[backend.ID]
		if known && previous == online {
			continue
		}
		if err := m.notify(ctx, backend.ID, online); err != nil {
			if m.logger != nil {
				m.logger.Warn("availability notification failed", "backend_id", backend.ID, "online", online, "error", err)
			}
			continue
		}
		m.sent[backend.ID] = online
		if m.logger != nil {
			m.logger.Info("availability changed", "backend_id", backend.ID, "online", online)
		}
	}
}

func (m *monitor) notify(ctx context.Context, id string, online bool) error {
	state := "offline"
	if online {
		state = "online"
	}
	body, _ := json.Marshal(map[string]string{"availability": state})
	url := strings.TrimRight(m.cfg.ControlURL, "/") + "/v1/backends/" + id + "/availability"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("control companion returned %s", resp.Status)
	}
	return nil
}

// Probe performs a Java status request. A TCP listener alone is not online.
func Probe(ctx context.Context, address, serverAddress string) error {
	host, portRaw, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.ParseUint(portRaw, 10, 16)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	payload := append(mcproto.WriteVarInt(mcproto.HandshakePacketID), mcproto.WriteVarInt(statusProtocolVersion)...)
	payload = append(payload, mcproto.EncodeString(serverAddress)...)
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	payload = append(payload, portBytes[:]...)
	payload = append(payload, mcproto.WriteVarInt(mcproto.NextStateStatus)...)
	handshake := mcproto.BuildPacket(0, payload[1:])
	if _, err := conn.Write(append(handshake, mcproto.BuildPacket(mcproto.StatusRequestPacketID)...)); err != nil {
		return err
	}
	id, payload, err := mcproto.ReadPacket(conn, mcproto.DefaultLimits().MaxPacketLength)
	if err != nil {
		return err
	}
	if id != mcproto.StatusResponsePacketID {
		return fmt.Errorf("unexpected status packet %d", id)
	}
	length, offset, err := readVarInt(payload)
	if err != nil || length < 1 || int(length) != len(payload)-offset {
		return fmt.Errorf("invalid status response")
	}
	if !json.Valid(payload[offset:]) {
		return fmt.Errorf("status response is not JSON")
	}
	_ = host
	return nil
}

func readVarInt(data []byte) (int32, int, error) {
	var value int32
	for index, b := range data {
		if index == 5 {
			return 0, 0, fmt.Errorf("varint too long")
		}
		value |= int32(b&0x7f) << (7 * index)
		if b&0x80 == 0 {
			return value, index + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("truncated varint")
}
