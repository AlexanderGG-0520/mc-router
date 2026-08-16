package proxyprotocol

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"strings"
)

const (
	maxV1 = 108
	maxV2 = 512
)

var v2sig = []byte("\r\n\r\n\x00\r\nQUIT\n")

func ParseCIDRs(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if addr, err := netip.ParseAddr(value); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr.Unmap(), addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q", value)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func Trusted(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Read consumes exactly one PROXY v1 or v2 header and returns its source IP.
func Read(r io.Reader) (netip.Addr, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return netip.Addr{}, err
	}
	if first[0] == 'P' {
		return readV1(r, first[0])
	}
	return readV2(r, first[0])
}

func readV1(r io.Reader, first byte) (netip.Addr, error) {
	line := []byte{first}
	for len(line) < maxV1 {
		var next [1]byte
		if _, err := io.ReadFull(r, next[:]); err != nil {
			return netip.Addr{}, err
		}
		line = append(line, next[0])
		if len(line) >= 2 && line[len(line)-2] == '\r' && line[len(line)-1] == '\n' {
			break
		}
	}
	if len(line) < 2 || line[len(line)-2] != '\r' || line[len(line)-1] != '\n' {
		return netip.Addr{}, fmt.Errorf("oversized PROXY v1 header")
	}
	fields := strings.Split(string(line[:len(line)-2]), " ")
	if len(fields) != 6 || fields[0] != "PROXY" || (fields[1] != "TCP4" && fields[1] != "TCP6") {
		return netip.Addr{}, fmt.Errorf("invalid PROXY v1 header")
	}
	addr, err := netip.ParseAddr(fields[2])
	if err != nil || (fields[1] == "TCP4" && !addr.Is4()) || (fields[1] == "TCP6" && !addr.Is6()) {
		return netip.Addr{}, fmt.Errorf("invalid PROXY v1 source")
	}
	return addr.Unmap(), nil
}

func readV2(r io.Reader, first byte) (netip.Addr, error) {
	header := make([]byte, 16)
	header[0] = first
	if _, err := io.ReadFull(r, header[1:]); err != nil {
		return netip.Addr{}, err
	}
	if string(header[:12]) != string(v2sig) || header[12] != 0x21 {
		return netip.Addr{}, fmt.Errorf("invalid PROXY v2 header")
	}
	length := int(binary.BigEndian.Uint16(header[14:]))
	if length > maxV2 {
		return netip.Addr{}, fmt.Errorf("oversized PROXY v2 header")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return netip.Addr{}, err
	}
	switch header[13] {
	case 0x11:
		if len(payload) < 12 {
			return netip.Addr{}, fmt.Errorf("short PROXY v2 IPv4 header")
		}
		return netip.AddrFrom4([4]byte(payload[:4])), nil
	case 0x21:
		if len(payload) < 36 {
			return netip.Addr{}, fmt.Errorf("short PROXY v2 IPv6 header")
		}
		var addr [16]byte
		copy(addr[:], payload[:16])
		return netip.AddrFrom16(addr), nil
	default:
		return netip.Addr{}, fmt.Errorf("unsupported PROXY v2 family")
	}
}
