package hostaddr

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

const MaxHostBytes = 253

var ErrInvalid = errors.New("invalid server address")

func Normalize(address string) (string, error) {
	first := address
	if idx := strings.IndexByte(first, 0); idx >= 0 {
		first = first[:idx]
	}
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(first)), ".")
	if normalized == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalid)
	}
	if len(normalized) > MaxHostBytes {
		return "", fmt.Errorf("%w: length %d exceeds %d", ErrInvalid, len(normalized), MaxHostBytes)
	}
	if !utf8.ValidString(normalized) {
		return "", fmt.Errorf("%w: not valid utf-8", ErrInvalid)
	}

	ipCandidate := strings.TrimPrefix(strings.TrimSuffix(normalized, "]"), "[")
	if ip := net.ParseIP(ipCandidate); ip != nil {
		return ipCandidate, nil
	}

	labels := strings.Split(normalized, ".")
	for _, label := range labels {
		if len(label) == 0 {
			return "", fmt.Errorf("%w: empty label", ErrInvalid)
		}
		if len(label) > 63 {
			return "", fmt.Errorf("%w: label length %d exceeds 63", ErrInvalid, len(label))
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("%w: label starts or ends with hyphen", ErrInvalid)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return "", fmt.Errorf("%w: unsupported character %q", ErrInvalid, c)
		}
	}
	return normalized, nil
}
