package ratelimit

import (
	"net/netip"
	"testing"
	"time"
)

func TestLimiterLimitsEachClientIndependently(t *testing.T) {
	limiter := New(Config{Enabled: true, ConnectionsPerSecond: 1, Burst: 1, IdleTimeout: time.Minute, MaxEntries: 2})
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	first := netip.MustParseAddr("203.0.113.1")
	second := netip.MustParseAddr("203.0.113.2")
	if !limiter.Allow(first) {
		t.Fatal("first client was rejected")
	}
	if limiter.Allow(first) {
		t.Fatal("first client exceeded its burst")
	}
	if !limiter.Allow(second) {
		t.Fatal("second client was affected by first client")
	}
}

func TestLimiterRefillsTokens(t *testing.T) {
	limiter := New(Config{Enabled: true, ConnectionsPerSecond: 2, Burst: 2, IdleTimeout: time.Minute, MaxEntries: 2})
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	addr := netip.MustParseAddr("203.0.113.1")
	if !limiter.Allow(addr) || !limiter.Allow(addr) || limiter.Allow(addr) {
		t.Fatal("unexpected initial token behavior")
	}
	now = now.Add(500 * time.Millisecond)
	if !limiter.Allow(addr) {
		t.Fatal("one token did not refill")
	}
}

func TestLimiterEvictsOldestEntryWhenFull(t *testing.T) {
	limiter := New(Config{Enabled: true, ConnectionsPerSecond: 1, Burst: 1, IdleTimeout: time.Hour, MaxEntries: 2})
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	first := netip.MustParseAddr("203.0.113.1")
	second := netip.MustParseAddr("203.0.113.2")
	third := netip.MustParseAddr("203.0.113.3")
	if !limiter.Allow(first) {
		t.Fatal("first client was rejected")
	}
	now = now.Add(time.Second)
	if !limiter.Allow(second) || !limiter.Allow(third) {
		t.Fatal("new client was rejected when full")
	}
	if len(limiter.entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(limiter.entries))
	}
	if _, ok := limiter.entries[first]; ok {
		t.Fatal("oldest entry was not evicted")
	}
}

func TestLimiterExpiresIdleEntries(t *testing.T) {
	limiter := New(Config{Enabled: true, ConnectionsPerSecond: 1, Burst: 1, IdleTimeout: time.Minute, MaxEntries: 2})
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	first := netip.MustParseAddr("203.0.113.1")
	second := netip.MustParseAddr("203.0.113.2")
	if !limiter.Allow(first) {
		t.Fatal("first client was rejected")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow(second) {
		t.Fatal("second client was rejected")
	}
	if len(limiter.entries) != 1 {
		t.Fatalf("entry count after expiry = %d, want 1", len(limiter.entries))
	}
}

func TestDisabledLimiterAllowsInvalidAddress(t *testing.T) {
	limiter := New(Config{})
	if !limiter.Allow(netip.Addr{}) {
		t.Fatal("disabled limiter rejected invalid address")
	}
}
