package mcproto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBuildStatusResponsePacket(t *testing.T) {
	packet, err := BuildStatusResponsePacket(StatusResponse{
		Version: StatusVersion{
			Name:     "mc-gateway",
			Protocol: 767,
		},
		Players: StatusPlayers{
			Max:    10,
			Online: 2,
		},
		Description: StatusChatComponent{
			Text: `Server "unavailable"`,
		},
	})
	if err != nil {
		t.Fatalf("BuildStatusResponsePacket: %v", err)
	}
	packetID, payload, err := ReadPacket(bytes.NewReader(packet), DefaultLimits().MaxPacketLength)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if packetID != StatusResponsePacketID {
		t.Fatalf("packet id = %d, want %d", packetID, StatusResponsePacketID)
	}
	statusJSON, remaining, err := readStatusString(payload)
	if err != nil {
		t.Fatalf("read status string: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("status response has %d trailing bytes", len(remaining))
	}
	var status StatusResponse
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("Unmarshal status JSON: %v", err)
	}
	if status.Description.Text != `Server "unavailable"` {
		t.Fatalf("description text = %q", status.Description.Text)
	}
	if status.Version.Protocol != 767 {
		t.Fatalf("protocol = %d", status.Version.Protocol)
	}
}

func readStatusString(payload []byte) (string, []byte, error) {
	reader := bytes.NewReader(payload)
	value, err := readString(reader, int32(len(payload)))
	if err != nil {
		return "", nil, err
	}
	return value, payload[len(payload)-reader.Len():], nil
}
