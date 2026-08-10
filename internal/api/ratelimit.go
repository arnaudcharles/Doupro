package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a simple fixed-window limiter keyed by client IP, used to
// blunt brute-force attempts against authentication endpoints (SECURITY.md
// requires this; nothing else in this single-instance daemon needs a
// distributed limiter, so an in-memory map is enough — see
// docs/architecture.md, DoUpRo is not designed to run more than one
// replica).
type rateLimiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	count      int
	windowEnds time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, buckets: make(map[string]*bucket)}
}

// allow reports whether key (a client IP) is still under the limit for the
// current window, incrementing its count either way so repeated calls
// after the limit is hit keep being counted rather than resetting it.
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.windowEnds) {
		b = &bucket{windowEnds: now.Add(l.window)}
		l.buckets[key] = b
	}
	b.count++
	return b.count <= l.limit
}

// wrap rejects requests over the limit with 429 before they reach next,
// keying on the client IP (not the attempted username — an attacker
// enumerating usernames from one IP must still be slowed down).
func (l *rateLimiter) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
			return
		}
		next(w, r)
	}
}

// clientIP extracts the request's IP for rate-limiting purposes. It
// deliberately ignores X-Forwarded-For — trusting that header without a
// known, configured proxy in front would let an attacker forge a fresh IP
// on every request and bypass the limiter entirely.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
