package plugin

import (
	"sync"
	"time"
)

// bucket is a token bucket: perMinute tokens a minute, at most burst
// waiting. It never blocks: Take reports whether a token was there.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	rate   float64 // tokens a second
	last   time.Time
	now    func() time.Time
}

func newBucket(perMinute, burst int, now func() time.Time) *bucket {
	return &bucket{tokens: float64(burst), burst: float64(burst), rate: float64(perMinute) / 60, now: now, last: now()}
}

func (b *bucket) Take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.now()
	b.tokens += t.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = t
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// backoff is the delay before restart n (1-based): RestartMin doubling,
// at most RestartMax.
func backoff(n int) time.Duration {
	d := RestartMin
	for i := 1; i < n && d < RestartMax; i++ {
		d *= 2
	}
	if d > RestartMax {
		d = RestartMax
	}
	return d
}
