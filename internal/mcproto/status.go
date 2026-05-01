package mcproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	StatusRequestPacketID  = 0x00
	StatusResponsePacketID = 0x00
	StatusPingPacketID     = 0x01
	StatusPongPacketID     = 0x01
)

type StatusResponse struct {
	Version     StatusVersion       `json:"version"`
	Players     StatusPlayers       `json:"players"`
	Description StatusChatComponent `json:"description"`
}

type StatusVersion struct {
	Name     string `json:"name"`
	Protocol int    `json:"protocol"`
}

type StatusPlayers struct {
	Max    int `json:"max"`
	Online int `json:"online"`
}

type StatusChatComponent struct {
	Text string `json:"text"`
}

func ReadPacket(r io.Reader, maxPacketLength int32) (int32, []byte, error) {
	if maxPacketLength <= 0 {
		maxPacketLength = DefaultLimits().MaxPacketLength
	}
	packetLength, _, err := readVarIntFromReader(r)
	if err != nil {
		return 0, nil, err
	}
	if packetLength <= 0 || packetLength > maxPacketLength {
		return 0, nil, ErrPacketTooLarge
	}
	payload := make([]byte, packetLength)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, fmt.Errorf("read packet payload: %w", err)
	}
	reader := bytes.NewReader(payload)
	packetID, _, err := readVarIntFromReader(reader)
	if err != nil {
		return 0, nil, fmt.Errorf("packet id: %w", err)
	}
	remainingOffset := len(payload) - reader.Len()
	return packetID, payload[remainingOffset:], nil
}

func BuildPacket(packetID int32, fields ...[]byte) []byte {
	var payload []byte
	payload = append(payload, WriteVarInt(packetID)...)
	for _, field := range fields {
		payload = append(payload, field...)
	}
	return append(WriteVarInt(int32(len(payload))), payload...)
}

func BuildStatusResponsePacket(status StatusResponse) ([]byte, error) {
	raw, err := json.Marshal(status)
	if err != nil {
		return nil, fmt.Errorf("marshal status response: %w", err)
	}
	return BuildPacket(StatusResponsePacketID, EncodeString(string(raw))), nil
}

func BuildStatusPongPacket(payload []byte) []byte {
	return BuildPacket(StatusPongPacketID, payload)
}

func EncodeString(value string) []byte {
	raw := []byte(value)
	out := WriteVarInt(int32(len(raw)))
	out = append(out, raw...)
	return out
}

func EncodeLong(value int64) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], uint64(value))
	return out[:]
}
