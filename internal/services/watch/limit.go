// Token bucket per remote address, after the idea of andon
// internal/services/hooks/hooks.go (468474a): a leaked URL must not be
// able to flood the database. No code is copied; Andon counts a sliding
// minute per connection, here every address gets a bucket.

package watch

import (
	"sync"
	"time"
)

const (
	pingRate  = 10.0 // pings per second, refilled
	pingBurst = 20.0
	// pruneAbove: when more addresses are known, full buckets are forgotten.
	pruneAbove = 4096
)

type bucket struct {
	tokens float64
	at     time.Time
}

type limiter struct {
	now     func() time.Time
	mu      sync.Mutex
	buckets map[string]*bucket
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, buckets: map[string]*bucket{}}
}

// allow takes one token of addr's bucket; false when it is empty.
func (l *limiter) allow(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.buckets[addr]
	if b == nil {
		if len(l.buckets) >= pruneAbove {
			l.prune(now)
		}
		b = &bucket{tokens: pingBurst, at: now}
		l.buckets[addr] = b
	}

	b.tokens += now.Sub(b.at).Seconds() * pingRate
	b.at = now
	if b.tokens > pingBurst {
		b.tokens = pingBurst
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// prune forgets buckets that have refilled completely: they carry no
// information.
func (l *limiter) prune(now time.Time) {
	for addr, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*pingRate >= pingBurst {
			delete(l.buckets, addr)
		}
	}
}
