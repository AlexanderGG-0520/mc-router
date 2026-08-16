package proxyprotocol

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestReadV1(t *testing.T) {
	addr, err := Read(bytes.NewBufferString("PROXY TCP4 203.0.113.7 192.0.2.1 12345 25565\r\nnext"))
	if err != nil { t.Fatal(err) }
	if want := netip.MustParseAddr("203.0.113.7"); addr != want { t.Fatalf("addr = %v, want %v", addr, want) }
}

func TestReadV2IPv6(t *testing.T) {
	payload := make([]byte, 36)
	source := netip.MustParseAddr("2001:db8::7").As16()
	copy(payload, source[:])
	header := append(append([]byte{}, v2sig...), 0x21, 0x21, 0, byte(len(payload)))
	addr, err := Read(bytes.NewReader(append(header, payload...)))
	if err != nil { t.Fatal(err) }
	if want := netip.MustParseAddr("2001:db8::7"); addr != want { t.Fatalf("addr = %v, want %v", addr, want) }
}

func TestReadRejectsInvalidHeader(t *testing.T) {
	if _, err := Read(bytes.NewReader([]byte("PROXY UNKNOWN\r\n"))); err == nil { t.Fatal("Read unexpectedly succeeded") }
}
