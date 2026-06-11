package voicechat

import (
	"errors"
	"testing"
)

func TestParseClientPacket(t *testing.T) {
	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	packet := append([]byte{MagicByte}, uuid[:]...)
	packet = append(packet, byte(len(payload)))
	packet = append(packet, payload...)

	parsed, err := ParseClientPacket(packet)
	if err != nil {
		t.Fatalf("ParseClientPacket returned error: %v", err)
	}

	if parsed.PlayerUUID != uuid {
		t.Fatalf("UUID mismatch: got %s want %s", parsed.PlayerUUID, uuid)
	}
	if string(parsed.EncryptedPayload) != string(payload) {
		t.Fatalf("payload mismatch: got %x want %x", parsed.EncryptedPayload, payload)
	}
}

func TestParseClientPacketZeroLengthPayload(t *testing.T) {
	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	packet := append([]byte{MagicByte}, uuid[:]...)
	packet = append(packet, 0)

	parsed, err := ParseClientPacket(packet)
	if err != nil {
		t.Fatalf("ParseClientPacket returned error: %v", err)
	}
	if len(parsed.EncryptedPayload) != 0 {
		t.Fatalf("payload length = %d, want 0", len(parsed.EncryptedPayload))
	}
}

func TestParseClientPacketMultiBytePayloadLength(t *testing.T) {
	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	payload := make([]byte, 300)
	for i := range payload {
		payload[i] = byte(i)
	}

	packet := append([]byte{MagicByte}, uuid[:]...)
	packet = append(packet, 0xac, 0x02)
	packet = append(packet, payload...)

	parsed, err := ParseClientPacket(packet)
	if err != nil {
		t.Fatalf("ParseClientPacket returned error: %v", err)
	}
	if len(parsed.EncryptedPayload) != len(payload) {
		t.Fatalf("payload length = %d, want %d", len(parsed.EncryptedPayload), len(payload))
	}
}

func TestParseClientPacketErrors(t *testing.T) {
	uuid := mustParseUUID(t, "00112233-4455-6677-8899-aabbccddeeff")

	tests := []struct {
		name string
		data []byte
		want error
	}{
		{
			name: "empty",
			data: nil,
			want: ErrPacketTooShort,
		},
		{
			name: "bad magic",
			data: []byte{0x00},
			want: ErrInvalidMagic,
		},
		{
			name: "missing length",
			data: append([]byte{MagicByte}, uuid[:]...),
			want: ErrPacketTooShort,
		},
		{
			name: "truncated varint",
			data: append(append([]byte{MagicByte}, uuid[:]...), 0x80),
			want: ErrTruncatedPacket,
		},
		{
			name: "invalid varint",
			data: append(append([]byte{MagicByte}, uuid[:]...), 0x80, 0x80, 0x80, 0x80, 0x80),
			want: ErrInvalidLength,
		},
		{
			name: "truncated payload",
			data: append(append([]byte{MagicByte}, uuid[:]...), 2, 0x01),
			want: ErrTruncatedPacket,
		},
		{
			name: "trailing bytes",
			data: append(append([]byte{MagicByte}, uuid[:]...), 1, 0x01, 0x02),
			want: ErrTrailingBytes,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseClientPacket(tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestUUIDStringAndParse(t *testing.T) {
	const value = "00112233-4455-6677-8899-aabbccddeeff"

	uuid, err := ParseUUID(value)
	if err != nil {
		t.Fatalf("ParseUUID returned error: %v", err)
	}
	if uuid.String() != value {
		t.Fatalf("String() = %q, want %q", uuid.String(), value)
	}
}

func TestParseUUIDErrors(t *testing.T) {
	for _, value := range []string{"", "00112233-4455-6677-8899-aabbccddeef", "not-a-uuid"} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseUUID(value); err == nil {
				t.Fatal("ParseUUID returned nil error")
			}
		})
	}
}

func mustParseUUID(t *testing.T, value string) UUID {
	t.Helper()

	uuid, err := ParseUUID(value)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", value, err)
	}
	return uuid
}
