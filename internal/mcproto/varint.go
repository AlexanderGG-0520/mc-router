package mcproto

import (
	"errors"
	"fmt"
	"io"
)

const MaxVarIntBytes = 5

var ErrVarIntTooLong = errors.New("varint is too long")

type byteReader struct {
	r   io.Reader
	buf [1]byte
}

func (r *byteReader) ReadByte() (byte, error) {
	_, err := io.ReadFull(r.r, r.buf[:])
	return r.buf[0], err
}

func ReadVarInt(r io.ByteReader) (int32, []byte, error) {
	var value int32
	raw := make([]byte, 0, MaxVarIntBytes)
	for i := 0; i < MaxVarIntBytes; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, raw, err
		}
		raw = append(raw, b)
		value |= int32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return value, raw, nil
		}
	}
	return 0, raw, ErrVarIntTooLong
}

func WriteVarInt(value int32) []byte {
	var out []byte
	for {
		temp := byte(value & 0x7f)
		value = int32(uint32(value) >> 7)
		if value != 0 {
			temp |= 0x80
		}
		out = append(out, temp)
		if value == 0 {
			return out
		}
	}
}

func readVarIntFromReader(r io.Reader) (int32, []byte, error) {
	br, ok := r.(io.ByteReader)
	if !ok {
		br = &byteReader{r: r}
	}
	value, raw, err := ReadVarInt(br)
	if err != nil {
		return 0, raw, fmt.Errorf("read varint: %w", err)
	}
	return value, raw, nil
}
