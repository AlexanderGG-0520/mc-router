package voicechat

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// MagicByte is the clear Simple Voice Chat UDP packet prefix.
	MagicByte byte = 0xff

	uuidSize       = 16
	maxVarIntBytes = 5
)

var (
	ErrPacketTooShort  = errors.New("voicechat packet too short")
	ErrInvalidMagic    = errors.New("voicechat packet has invalid magic byte")
	ErrInvalidLength   = errors.New("voicechat packet has invalid encrypted payload length")
	ErrTruncatedPacket = errors.New("voicechat packet is truncated")
	ErrTrailingBytes   = errors.New("voicechat packet has trailing bytes")
)

// UUID is the clear 128-bit player identifier carried in client-to-server
// Simple Voice Chat UDP datagrams.
type UUID [uuidSize]byte

func (u UUID) String() string {
	var dst [36]byte
	hex.Encode(dst[0:8], u[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], u[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], u[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], u[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], u[10:16])
	return string(dst[:])
}

// ClientPacket is the visible, non-decrypted envelope of a client-to-server
// Simple Voice Chat UDP datagram.
type ClientPacket struct {
	PlayerUUID       UUID
	EncryptedPayload []byte
}

// ParseClientPacket extracts only fields that are visible before Simple Voice
// Chat decrypts the UDP payload. It does not inspect packet type, authentication
// data, audio data, or the voice secret.
func ParseClientPacket(packet []byte) (ClientPacket, error) {
	if len(packet) < 1 {
		return ClientPacket{}, ErrPacketTooShort
	}
	if packet[0] != MagicByte {
		return ClientPacket{}, ErrInvalidMagic
	}
	if len(packet) < 1+uuidSize+1 {
		return ClientPacket{}, ErrPacketTooShort
	}

	var playerUUID UUID
	copy(playerUUID[:], packet[1:1+uuidSize])

	payloadOffset := 1 + uuidSize
	payloadLen, varIntLen, err := readVarInt(packet[payloadOffset:])
	if err != nil {
		return ClientPacket{}, err
	}
	payloadOffset += varIntLen

	if payloadLen < 0 {
		return ClientPacket{}, ErrInvalidLength
	}
	if len(packet)-payloadOffset < payloadLen {
		return ClientPacket{}, ErrTruncatedPacket
	}
	if len(packet)-payloadOffset > payloadLen {
		return ClientPacket{}, ErrTrailingBytes
	}

	return ClientPacket{
		PlayerUUID:       playerUUID,
		EncryptedPayload: packet[payloadOffset : payloadOffset+payloadLen],
	}, nil
}

func readVarInt(data []byte) (int, int, error) {
	var value int32
	for i := 0; i < maxVarIntBytes; i++ {
		if i >= len(data) {
			return 0, 0, ErrTruncatedPacket
		}

		b := data[i]
		value |= int32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			if value < 0 {
				return 0, 0, ErrInvalidLength
			}
			return int(value), i + 1, nil
		}
	}
	return 0, 0, ErrInvalidLength
}

// ParseUUID converts a canonical UUID string into the package UUID type.
func ParseUUID(s string) (UUID, error) {
	cleaned := strings.ReplaceAll(s, "-", "")
	if len(cleaned) != uuidSize*2 {
		return UUID{}, fmt.Errorf("invalid UUID length")
	}

	decoded, err := hex.DecodeString(cleaned)
	if err != nil {
		return UUID{}, fmt.Errorf("invalid UUID: %w", err)
	}

	var uuid UUID
	copy(uuid[:], decoded)
	return uuid, nil
}
