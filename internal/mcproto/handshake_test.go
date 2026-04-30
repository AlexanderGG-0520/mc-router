package mcproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestReadHandshake(t *testing.T) {
	raw := buildHandshakePacket(t, 765, "SMP.Example.COM.", 25565, NextStateLogin)
	got, preserved, err := ReadHandshake(bytes.NewReader(raw), DefaultLimits())
	if err != nil {
		t.Fatalf("ReadHandshake returned error: %v", err)
	}
	if !bytes.Equal(preserved, raw) {
		t.Fatalf("preserved bytes changed")
	}
	if got.ProtocolVersion != 765 {
		t.Fatalf("protocol version = %d", got.ProtocolVersion)
	}
	if got.ServerAddress != "SMP.Example.COM." {
		t.Fatalf("server address = %q", got.ServerAddress)
	}
	if got.RouteAddress() != "smp.example.com" {
		t.Fatalf("route address = %q", got.RouteAddress())
	}
	if got.ServerPort != 25565 {
		t.Fatalf("server port = %d", got.ServerPort)
	}
	if got.NextState != NextStateLogin {
		t.Fatalf("next state = %d", got.NextState)
	}
}

func TestReadHandshakeRejectsPacketTooLarge(t *testing.T) {
	raw := append(WriteVarInt(10), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}...)
	_, _, err := ReadHandshake(bytes.NewReader(raw), Limits{MaxPacketLength: 4, MaxServerAddressBytes: 255})
	if !errors.Is(err, ErrPacketTooLarge) {
		t.Fatalf("error = %v, want %v", err, ErrPacketTooLarge)
	}
}

func TestReadHandshakeRejectsLongAddress(t *testing.T) {
	raw := buildHandshakePacket(t, 765, "too-long.example.com", 25565, NextStateLogin)
	_, _, err := ReadHandshake(bytes.NewReader(raw), Limits{MaxPacketLength: 2048, MaxServerAddressBytes: 3})
	if !errors.Is(err, ErrMalformedHandshake) {
		t.Fatalf("error = %v, want %v", err, ErrMalformedHandshake)
	}
}

func TestReadHandshakeRejectsTrailingBytes(t *testing.T) {
	raw := buildHandshakePacket(t, 765, "smp.example.com", 25565, NextStateLogin)
	raw = append(raw, 0x01)
	raw[0]++
	_, _, err := ReadHandshake(bytes.NewReader(raw), DefaultLimits())
	if !errors.Is(err, ErrMalformedHandshake) {
		t.Fatalf("error = %v, want %v", err, ErrMalformedHandshake)
	}
}

func TestReadHandshakeRejectsMalformedVarInt(t *testing.T) {
	_, _, err := ReadHandshake(bytes.NewReader([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x00}), DefaultLimits())
	if !errors.Is(err, ErrVarIntTooLong) {
		t.Fatalf("error = %v, want %v", err, ErrVarIntTooLong)
	}
}

func TestReadHandshakeRejectsTruncatedPacket(t *testing.T) {
	raw := append(WriteVarInt(10), []byte{0x00, 0x01}...)
	_, _, err := ReadHandshake(bytes.NewReader(raw), DefaultLimits())
	if err == nil {
		t.Fatal("expected truncated packet error")
	}
}

func TestReadHandshakeRejectsUnsupportedPacketID(t *testing.T) {
	raw := buildHandshakePacketWithPacketID(t, 0x01, 765, "smp.example.com", 25565, NextStateLogin)
	_, _, err := ReadHandshake(bytes.NewReader(raw), DefaultLimits())
	if !errors.Is(err, ErrMalformedHandshake) {
		t.Fatalf("error = %v, want %v", err, ErrMalformedHandshake)
	}
}

func TestReadHandshakeRejectsInvalidNextState(t *testing.T) {
	raw := buildHandshakePacket(t, 765, "smp.example.com", 25565, 99)
	_, _, err := ReadHandshake(bytes.NewReader(raw), DefaultLimits())
	if !errors.Is(err, ErrUnsupportedNextState) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedNextState)
	}
}

func TestReadHandshakeRejectsInvalidServerAddress(t *testing.T) {
	tests := []string{
		"",
		".",
		"-bad.example.com",
		"bad_.example.com",
		"bad example.com",
	}
	for _, address := range tests {
		t.Run(address, func(t *testing.T) {
			raw := buildHandshakePacket(t, 765, address, 25565, NextStateLogin)
			_, _, err := ReadHandshake(bytes.NewReader(raw), DefaultLimits())
			if !errors.Is(err, ErrMalformedHandshake) {
				t.Fatalf("error = %v, want %v", err, ErrMalformedHandshake)
			}
		})
	}
}

func buildHandshakePacket(t *testing.T, protocol int32, address string, port uint16, nextState int32) []byte {
	t.Helper()
	return buildHandshakePacketWithPacketID(t, HandshakePacketID, protocol, address, port, nextState)
}

func buildHandshakePacketWithPacketID(t *testing.T, packetID int32, protocol int32, address string, port uint16, nextState int32) []byte {
	t.Helper()
	var payload []byte
	payload = append(payload, WriteVarInt(packetID)...)
	payload = append(payload, WriteVarInt(protocol)...)
	payload = append(payload, WriteVarInt(int32(len(address)))...)
	payload = append(payload, []byte(address)...)
	var portRaw [2]byte
	binary.BigEndian.PutUint16(portRaw[:], port)
	payload = append(payload, portRaw[:]...)
	payload = append(payload, WriteVarInt(nextState)...)
	return append(WriteVarInt(int32(len(payload))), payload...)
}
