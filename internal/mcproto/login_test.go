package mcproto

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func TestValidateLoginStartPayloadAcceptsProtocol767(t *testing.T) {
	payload := append(EncodeString("PlayerOne"), make([]byte, 16)...)
	if err := ValidateLoginStartPayload(ProtocolMinecraft1211, payload); err != nil {
		t.Fatalf("ValidateLoginStartPayload returned error: %v", err)
	}
}

func TestValidateLoginStartPayloadRejectsMalformed(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty username", payload: append(EncodeString(""), make([]byte, 16)...)},
		{name: "missing uuid", payload: EncodeString("PlayerOne")},
		{name: "wrong uuid length", payload: append(EncodeString("PlayerOne"), make([]byte, 15)...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateLoginStartPayload(ProtocolMinecraft1211, tt.payload); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestValidateLoginStartPayloadRejectsUnsupportedProtocol(t *testing.T) {
	payload := append(EncodeString("PlayerOne"), make([]byte, 16)...)
	if err := ValidateLoginStartPayload(765, payload); err == nil {
		t.Fatal("expected unsupported protocol error")
	}
}

func TestBuildLoginDisconnectPacketEscapesMessage(t *testing.T) {
	packetID, payload, err := ReadPacket(bytes.NewReader(mustBuildLoginDisconnectPacket(t, `Server "unavailable"`)), DefaultLimits().MaxPacketLength)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if packetID != LoginDisconnectPacketID {
		t.Fatalf("packet id = %d, want %d", packetID, LoginDisconnectPacketID)
	}
	reasonJSON, remaining := readStringPayload(t, payload)
	if len(remaining) != 0 {
		t.Fatalf("remaining payload bytes = %d", len(remaining))
	}
	var reason StatusChatComponent
	if err := json.Unmarshal([]byte(reasonJSON), &reason); err != nil {
		t.Fatalf("Unmarshal reason: %v", err)
	}
	if reason.Text != `Server "unavailable"` {
		t.Fatalf("reason text = %q", reason.Text)
	}
}

func mustBuildLoginDisconnectPacket(t *testing.T, message string) []byte {
	t.Helper()
	packet, err := BuildLoginDisconnectPacket(ProtocolMinecraft1211, message)
	if err != nil {
		t.Fatalf("BuildLoginDisconnectPacket: %v", err)
	}
	return packet
}

func readStringPayload(t *testing.T, payload []byte) (string, []byte) {
	t.Helper()
	reader := bytes.NewReader(payload)
	value, err := readString(reader, int32(len(payload)))
	if err != nil {
		t.Fatalf("readString: %v", err)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return value, remaining
}
