package mcproto

import (
	"bytes"
	"errors"
	"strconv"
	"testing"
)

func TestReadVarInt(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want int32
	}{
		{name: "zero", raw: []byte{0x00}, want: 0},
		{name: "one", raw: []byte{0x01}, want: 1},
		{name: "two bytes", raw: []byte{0xac, 0x02}, want: 300},
		{name: "max int", raw: []byte{0xff, 0xff, 0xff, 0xff, 0x07}, want: 2147483647},
		{name: "negative one", raw: []byte{0xff, 0xff, 0xff, 0xff, 0x0f}, want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, raw, err := ReadVarInt(bytes.NewReader(tt.raw))
			if err != nil {
				t.Fatalf("ReadVarInt returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("value = %d, want %d", got, tt.want)
			}
			if !bytes.Equal(raw, tt.raw) {
				t.Fatalf("raw = %v, want %v", raw, tt.raw)
			}
		})
	}
}

func TestReadVarIntRejectsTooLong(t *testing.T) {
	_, _, err := ReadVarInt(bytes.NewReader([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x00}))
	if !errors.Is(err, ErrVarIntTooLong) {
		t.Fatalf("error = %v, want %v", err, ErrVarIntTooLong)
	}
}

func TestWriteVarIntRoundTrip(t *testing.T) {
	for _, value := range []int32{0, 1, 127, 128, 255, 300, 2147483647, -1} {
		t.Run(strconv.FormatInt(int64(value), 10), func(t *testing.T) {
			got, _, err := ReadVarInt(bytes.NewReader(WriteVarInt(value)))
			if err != nil {
				t.Fatalf("ReadVarInt returned error: %v", err)
			}
			if got != value {
				t.Fatalf("value = %d, want %d", got, value)
			}
		})
	}
}
