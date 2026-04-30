package mcproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
)

const (
	HandshakePacketID = 0x00
	NextStateStatus   = 1
	NextStateLogin    = 2
)

var (
	ErrPacketTooLarge       = errors.New("minecraft packet is too large")
	ErrMalformedHandshake   = errors.New("malformed minecraft handshake")
	ErrUnsupportedNextState = errors.New("unsupported minecraft next state")
)

type Limits struct {
	MaxPacketLength       int32
	MaxServerAddressBytes int32
}

func DefaultLimits() Limits {
	return Limits{
		MaxPacketLength:       2048,
		MaxServerAddressBytes: 255,
	}
}

type Handshake struct {
	ProtocolVersion int32
	ServerAddress   string
	ServerPort      uint16
	NextState       int32
}

func (h Handshake) RouteAddress() string {
	normalized, err := hostaddr.Normalize(h.ServerAddress)
	if err != nil {
		return ""
	}
	return normalized
}

func ReadHandshake(r io.Reader, limits Limits) (Handshake, []byte, error) {
	if limits.MaxPacketLength <= 0 {
		limits.MaxPacketLength = DefaultLimits().MaxPacketLength
	}
	if limits.MaxServerAddressBytes <= 0 {
		limits.MaxServerAddressBytes = DefaultLimits().MaxServerAddressBytes
	}

	packetLength, lengthRaw, err := readVarIntFromReader(r)
	if err != nil {
		return Handshake{}, lengthRaw, err
	}
	if packetLength <= 0 || packetLength > limits.MaxPacketLength {
		return Handshake{}, lengthRaw, ErrPacketTooLarge
	}

	payload := make([]byte, packetLength)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Handshake{}, append(lengthRaw, payload...), fmt.Errorf("read handshake payload: %w", err)
	}
	raw := append(append([]byte{}, lengthRaw...), payload...)

	handshake, err := ParseHandshakePayload(payload, limits)
	if err != nil {
		return Handshake{}, raw, err
	}
	return handshake, raw, nil
}

func ParseHandshakePayload(payload []byte, limits Limits) (Handshake, error) {
	if limits.MaxServerAddressBytes <= 0 {
		limits.MaxServerAddressBytes = DefaultLimits().MaxServerAddressBytes
	}

	r := bytes.NewReader(payload)
	packetID, _, err := readVarIntFromReader(r)
	if err != nil {
		return Handshake{}, fmt.Errorf("%w: packet id: %w", ErrMalformedHandshake, err)
	}
	if packetID != HandshakePacketID {
		return Handshake{}, fmt.Errorf("%w: packet id %d", ErrMalformedHandshake, packetID)
	}

	protocolVersion, _, err := readVarIntFromReader(r)
	if err != nil {
		return Handshake{}, fmt.Errorf("%w: protocol version: %w", ErrMalformedHandshake, err)
	}
	serverAddress, err := readString(r, limits.MaxServerAddressBytes)
	if err != nil {
		return Handshake{}, fmt.Errorf("%w: server address: %w", ErrMalformedHandshake, err)
	}
	if strings.TrimSpace(serverAddress) == "" {
		return Handshake{}, fmt.Errorf("%w: server address is empty", ErrMalformedHandshake)
	}
	if _, err := hostaddr.Normalize(serverAddress); err != nil {
		return Handshake{}, fmt.Errorf("%w: server address: %w", ErrMalformedHandshake, err)
	}

	var portRaw [2]byte
	if _, err := io.ReadFull(r, portRaw[:]); err != nil {
		return Handshake{}, fmt.Errorf("%w: server port: %w", ErrMalformedHandshake, err)
	}
	nextState, _, err := readVarIntFromReader(r)
	if err != nil {
		return Handshake{}, fmt.Errorf("%w: next state: %w", ErrMalformedHandshake, err)
	}
	if nextState != NextStateStatus && nextState != NextStateLogin {
		return Handshake{}, fmt.Errorf("%w: %d", ErrUnsupportedNextState, nextState)
	}
	if r.Len() != 0 {
		return Handshake{}, fmt.Errorf("%w: trailing bytes", ErrMalformedHandshake)
	}

	return Handshake{
		ProtocolVersion: protocolVersion,
		ServerAddress:   serverAddress,
		ServerPort:      binary.BigEndian.Uint16(portRaw[:]),
		NextState:       nextState,
	}, nil
}

func readString(r io.Reader, maxBytes int32) (string, error) {
	length, _, err := readVarIntFromReader(r)
	if err != nil {
		return "", err
	}
	if length < 0 || length > maxBytes {
		return "", fmt.Errorf("length %d exceeds limit %d", length, maxBytes)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	if !utf8.Valid(buf) {
		return "", errors.New("not valid utf-8")
	}
	return string(buf), nil
}
