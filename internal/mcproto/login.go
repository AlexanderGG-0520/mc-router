package mcproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ProtocolMinecraft1211 = 767

	LoginStartPacketID      = 0x00
	LoginDisconnectPacketID = 0x00

	loginUsernameMaxBytes = 16
	loginUUIDBytes        = 16
)

var (
	ErrMalformedLoginStart      = errors.New("malformed minecraft login start")
	ErrUnsupportedLoginProtocol = errors.New("unsupported minecraft login protocol")
)

func ValidateLoginStartPayload(protocolVersion int32, payload []byte) error {
	if protocolVersion != ProtocolMinecraft1211 {
		return fmt.Errorf("%w: %d", ErrUnsupportedLoginProtocol, protocolVersion)
	}
	reader := bytes.NewReader(payload)
	username, err := readString(reader, loginUsernameMaxBytes)
	if err != nil {
		return fmt.Errorf("%w: username: %w", ErrMalformedLoginStart, err)
	}
	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("%w: username is empty", ErrMalformedLoginStart)
	}
	if reader.Len() != loginUUIDBytes {
		return fmt.Errorf("%w: uuid bytes %d", ErrMalformedLoginStart, reader.Len())
	}
	return nil
}

func BuildLoginDisconnectPacket(protocolVersion int32, message string) ([]byte, error) {
	packetID, err := LoginDisconnectPacketIDForProtocol(protocolVersion)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(StatusChatComponent{Text: message})
	if err != nil {
		return nil, fmt.Errorf("marshal login disconnect reason: %w", err)
	}
	field := append(WriteVarInt(int32(len(raw))), raw...)
	return BuildPacket(packetID, field), nil
}

func LoginDisconnectPacketIDForProtocol(protocolVersion int32) (int32, error) {
	if protocolVersion != ProtocolMinecraft1211 {
		return 0, fmt.Errorf("%w: %d", ErrUnsupportedLoginProtocol, protocolVersion)
	}
	return LoginDisconnectPacketID, nil
}
