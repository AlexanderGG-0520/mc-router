package clientpolicy

import (
	"net/netip"
	"testing"
)

func TestPolicyAllowsByDefault(t *testing.T) {
	policy, err := New(nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !policy.Allows(netip.MustParseAddr("203.0.113.10")) {
		t.Fatal("default policy denied client")
	}
}

func TestPolicyDeniesConfiguredCIDR(t *testing.T) {
	policy, err := New(nil, []string{"203.0.113.0/24", "2001:db8::1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, value := range []string{"203.0.113.10", "2001:db8::1"} {
		if policy.Allows(netip.MustParseAddr(value)) {
			t.Fatalf("policy allowed denied address %s", value)
		}
	}
	if !policy.Allows(netip.MustParseAddr("2001:db8::2")) {
		t.Fatal("policy denied address outside the configured entries")
	}
}

func TestPolicyAllowListTakesPrecedenceOverDenyList(t *testing.T) {
	policy, err := New([]string{"203.0.113.0/24"}, []string{"203.0.113.10", "198.51.100.0/24"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !policy.Allows(netip.MustParseAddr("203.0.113.10")) {
		t.Fatal("allow list did not take precedence")
	}
	if policy.Allows(netip.MustParseAddr("198.51.100.10")) {
		t.Fatal("allow list admitted an address outside the allow list")
	}
}

func TestNewRejectsInvalidEntry(t *testing.T) {
	_, err := New([]string{"not-an-address"}, nil)
	if err == nil {
		t.Fatal("New accepted invalid allow entry")
	}
}
