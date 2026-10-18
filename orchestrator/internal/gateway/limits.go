package gateway

import (
	"sync"
	"time"
)

const (
	// maxRequestBody caps a client request body; more gets 413.
	maxRequestBody = 32 << 20
	// defaultMaxInflight is the per-token concurrency limit when
	// Config.MaxInflightPerToken is 0.
	defaultMaxInflight = 8
	// negCacheTTL and negCacheSize bound the unknown-token cache, which
	// answers a repeated bad token without a Postgres lookup. It does not
	// stop a flood of distinct made-up tokens: each one is a lookup. The
	// pgx pool size bounds concurrent lookups (the rest queue for a
	// connection) and the per-token in-flight cap bounds what valid tokens
	// add; there is no global limiter.
	negCacheTTL  = 30 * time.Second
	negCacheSize = 10000
)

// negCache remembers token hashes that were not found. When full, adding
// evicts an arbitrary entry; expired entries are dropped on lookup.
type negCache struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	m   map[string]time.Time // hash -> expiry
}

func newNegCache(ttl time.Duration, maxEntries int) *negCache {
	return &negCache{ttl: ttl, max: maxEntries, m: make(map[string]time.Time)}
}

func (c *negCache) has(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.m[key]
	if ok && !now.Before(exp) {
		delete(c.m, key)
		return false
	}
	return ok
}

func (c *negCache) add(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok && len(c.m) >= c.max {
		for k := range c.m {
			delete(c.m, k)
			break
		}
	}
	c.m[key] = now.Add(c.ttl)
}

// inflight counts requests in progress per token hash.
type inflight struct {
	mu  sync.Mutex
	max int
	n   map[string]int
}

func (l *inflight) acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[key] >= l.max {
		return false
	}
	l.n[key]++
	return true
}

func (l *inflight) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[key]--; l.n[key] <= 0 {
		delete(l.n, key)
	}
}
