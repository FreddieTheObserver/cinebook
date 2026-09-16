package httpapi

import (
	"sync"
	"time"
)

// limiter keeps a token bucket per key. It lives in one process, so behind a
// load balancer every replica enforces the limit on its own.
type limiter struct {
	rate  float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
	sweepAt int
}

type bucket struct {
	tokens float64
	seen   time.Time
}

const minSweepAt = 1024

func newLimiter(ratePerSecond float64, burst int) *limiter {
	return &limiter{
		rate:    ratePerSecond,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
		sweepAt: minSweepAt,
	}
}

// allow spends a token for key, or reports how long until one is available.
func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		l.sweep(now)
		b = &bucket{tokens: l.burst, seen: now}
		l.buckets[key] = b
	}
	b.tokens = l.level(b, now)
	// Callers read the clock before taking the lock, so now can trail seen.
	if now.After(b.seen) {
		b.seen = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

func (l *limiter) level(b *bucket, now time.Time) float64 {
	elapsed := max(0, now.Sub(b.seen))
	return min(l.burst, b.tokens+elapsed.Seconds()*l.rate)
}

// The key is client supplied, so the map must not grow without bound. A full
// bucket behaves exactly like a missing one, so dropping it loses nothing, and
// doubling the threshold keeps the sweep amortized constant per new key.
func (l *limiter) sweep(now time.Time) {
	if len(l.buckets) < l.sweepAt {
		return
	}
	for key, b := range l.buckets {
		if l.level(b, now) >= l.burst {
			delete(l.buckets, key)
		}
	}
	l.sweepAt = max(minSweepAt, 2*len(l.buckets))
}
