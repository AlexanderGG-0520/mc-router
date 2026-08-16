package clientpolicy

import (
	"fmt"
	"net/netip"
	"strings"
)

// Policy decides whether a client source address may establish a connection.
// When allow entries are configured, they take precedence over deny entries.
type Policy struct {
	allow []netip.Prefix
	deny  []netip.Prefix
}

// New parses configured IP addresses or CIDR prefixes into a connection policy.
func New(allow, deny []string) (*Policy, error) {
	parsedAllow, err := parsePrefixes("allow", allow)
	if err != nil {
		return nil, err
	}
	parsedDeny, err := parsePrefixes("deny", deny)
	if err != nil {
		return nil, err
	}
	return &Policy{allow: parsedAllow, deny: parsedDeny}, nil
}

// Allows reports whether an address may connect. An allow list, when present,
// is authoritative and therefore takes precedence over the deny list.
func (p *Policy) Allows(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	if len(p.allow) > 0 {
		return contains(p.allow, addr)
	}
	return !contains(p.deny, addr)
}

func parsePrefixes(kind string, values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for i, value := range values {
		prefix, err := parsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", kind, i, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func parsePrefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Prefix{}, fmt.Errorf("must not be empty")
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("must be an IP address or CIDR prefix: %w", err)
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func contains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
