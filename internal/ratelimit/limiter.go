package ratelimit

import (
	"net/netip"
	"sync"
	"time"
)

// Config controls a bounded set of per-client token buckets.
type Config struct {
	Enabled              bool
	ConnectionsPerSecond float64
	Burst                int
	IdleTimeout          time.Duration
	MaxEntries           int
}

// Limiter permits connection attempts independently for each client IP address.
// It is safe for concurrent use.
type Limiter struct {
	cfg     Config
	entries map[netip.Addr]entry
	now     func() time.Time
	mutex   sync.Mutex
}

type entry struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

// New creates a limiter. A disabled limiter always allows connections.
func New(cfg Config) *Limiter {
	return &Limiter{
		cfg:     cfg,
		entries: make(map[netip.Addr]entry),
		now:     time.Now,
	}
}

// Allow reports whether the client IP has an available connection token.
func (l *Limiter) Allow(addr netip.Addr) bool {
	if !l.cfg.Enabled || !addr.IsValid() {
		return !l.cfg.Enabled
	}
	return l.allowAt(addr.Unmap(), l.now())
}

func (l *Limiter) allowAt(addr netip.Addr, now time.Time) bool {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.deleteExpired(now)
	current, exists := l.entries[addr]
	if !exists {
		if len(l.entries) >= l.cfg.MaxEntries {
			l.deleteOldest()
		}
		current = entry{tokens: float64(l.cfg.Burst), lastRefill: now}
	}

	elapsed := now.Sub(current.lastRefill).Seconds()
	if elapsed > 0 {
		current.tokens = min(float64(l.cfg.Burst), current.tokens+elapsed*l.cfg.ConnectionsPerSecond)
		current.lastRefill = now
	}
	current.lastSeen = now
	if current.tokens < 1 {
		l.entries[addr] = current
		return false
	}
	current.tokens--
	l.entries[addr] = current
	return true
}

func (l *Limiter) deleteExpired(now time.Time) {
	for addr, current := range l.entries {
		if now.Sub(current.lastSeen) >= l.cfg.IdleTimeout {
			delete(l.entries, addr)
		}
	}
}

func (l *Limiter) deleteOldest() {
	var oldestAddr netip.Addr
	var oldest time.Time
	for addr, current := range l.entries {
		if oldestAddr.IsValid() && !current.lastSeen.Before(oldest) {
			continue
		}
		oldestAddr = addr
		oldest = current.lastSeen
	}
	if oldestAddr.IsValid() {
		delete(l.entries, oldestAddr)
	}
}
