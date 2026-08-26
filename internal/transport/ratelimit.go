package transport

import (
	"math"
	"sync"
	"time"
)

// bucket is a token bucket that refills lazily from a timestamp, so it costs no
// goroutine and no timer. Not safe for concurrent use; each instance is owned by
// exactly one goroutine, or guarded by the map that holds it.
type bucket struct {
	tokens float64
	burst  float64
	rate   float64 // tokens per second
	last   time.Time
}

func newBucket(burst int, rate float64) bucket {
	return bucket{tokens: float64(burst), burst: float64(burst), rate: rate}
}

// allow spends one token, refilling first. It returns false when the caller is
// over budget.
func (b *bucket) allow(now time.Time) bool {
	if b.last.IsZero() {
		b.last = now
	}
	if d := now.Sub(b.last); d > 0 {
		b.tokens = math.Min(b.burst, b.tokens+d.Seconds()*b.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// full reports whether the bucket has refilled completely, which is the signal
// that it carries no state worth keeping.
func (b *bucket) full(now time.Time) bool {
	if b.last.IsZero() {
		return true
	}
	return b.tokens+now.Sub(b.last).Seconds()*b.rate >= b.burst
}

// bucketMap is a keyed set of buckets — one per remote address — pruned by the
// shared sweep rather than by a per-key timer.
type bucketMap struct {
	mu    sync.Mutex
	burst int
	rate  float64
	m     map[string]*bucket
}

func newBucketMap(burst int, rate float64) *bucketMap {
	return &bucketMap{burst: burst, rate: rate, m: make(map[string]*bucket)}
}

func (bm *bucketMap) allow(key string, now time.Time) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	b := bm.m[key]
	if b == nil {
		nb := newBucket(bm.burst, bm.rate)
		b = &nb
		bm.m[key] = b
	}
	return b.allow(now)
}

// prune drops keys that have refilled to full: they are indistinguishable from a
// key that was never seen, so keeping them is a slow leak keyed by attacker-
// chosen strings.
func (bm *bucketMap) prune(now time.Time) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	for k, b := range bm.m {
		if b.full(now) {
			delete(bm.m, k)
		}
	}
}
