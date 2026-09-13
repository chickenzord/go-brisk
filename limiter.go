package brisk

import (
	"context"
	"crypto/rand"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// Limiter defines an interface for rate-limiting outgoing HTTP requests.
type Limiter interface {
	Wait(ctx context.Context) error
}

// TokenBucket implements a standard token bucket rate limiter without external dependencies.
type TokenBucket struct {
	mu         sync.Mutex
	rate       float64       // Tokens added per second
	burst      float64       // Max tokens in the bucket
	tokens     float64       // Current token count
	lastUpdate time.Time     // Last time tokens were replenished
}

// NewTokenBucket creates a new TokenBucket rate limiter.
func NewTokenBucket(requestsPerSec float64, burst int) *TokenBucket {
	if requestsPerSec <= 0 {
		requestsPerSec = 1.0
	}
	if burst <= 0 {
		burst = 1
	}

	return &TokenBucket{
		rate:       requestsPerSec,
		burst:      float64(burst),
		tokens:     float64(burst),
		lastUpdate: time.Now(),
	}
}

// Wait blocks until a token is available or the context is cancelled.
func (tb *TokenBucket) Wait(ctx context.Context) error {
	for {
		tb.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(tb.lastUpdate).Seconds()
		tb.lastUpdate = now

		// Replenish tokens based on elapsed time
		tb.tokens += elapsed * tb.rate
		if tb.tokens > tb.burst {
			tb.tokens = tb.burst
		}

		if tb.tokens >= 1.0 {
			tb.tokens -= 1.0
			tb.mu.Unlock()
			return nil
		}

		// Calculate wait time needed for 1 token
		needed := 1.0 - tb.tokens
		waitDuration := time.Duration((needed / tb.rate) * float64(time.Second))
		tb.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDuration):
		}
	}
}

// DelayJitter introduces an artificial sleep between requests within [min, max] duration.
type DelayJitter struct {
	mu       sync.Mutex
	min      time.Duration
	max      time.Duration
	lastCall time.Time
}

// NewDelayJitter creates a new DelayJitter limiter.
func NewDelayJitter(min, max time.Duration) *DelayJitter {
	if min < 0 {
		min = 0
	}
	if max < min {
		max = min
	}
	return &DelayJitter{
		min: min,
		max: max,
	}
}

// Wait pauses execution to maintain the randomized delay since the last call.
func (dj *DelayJitter) Wait(ctx context.Context) error {
	dj.mu.Lock()
	defer dj.mu.Unlock()

	if dj.min == 0 && dj.max == 0 {
		return nil
	}

	now := time.Now()
	var targetDelay time.Duration

	if dj.min == dj.max {
		targetDelay = dj.min
	} else {
		delta := dj.max - dj.min
		n, err := rand.Int(rand.Reader, big.NewInt(int64(delta)))
		if err != nil {
			targetDelay = dj.min
		} else {
			targetDelay = dj.min + time.Duration(n.Int64())
		}
	}

	timeSinceLast := now.Sub(dj.lastCall)
	if !dj.lastCall.IsZero() && timeSinceLast < targetDelay {
		sleepNeeded := targetDelay - timeSinceLast
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepNeeded):
		}
	}

	dj.lastCall = time.Now()
	return nil
}

// MultiLimiter sequences multiple limiters in order.
type MultiLimiter struct {
	limiters []Limiter
}

// NewMultiLimiter creates a combined limiter that runs each limiter sequentially.
func NewMultiLimiter(limiters ...Limiter) *MultiLimiter {
	var valid []Limiter
	for _, l := range limiters {
		if l != nil {
			valid = append(valid, l)
		}
	}
	return &MultiLimiter{limiters: valid}
}

// Wait executes Wait on each registered limiter.
func (m *MultiLimiter) Wait(ctx context.Context) error {
	for _, l := range m.limiters {
		if err := l.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

// WaitRequest executes WaitRequest or Wait on each registered limiter.
func (m *MultiLimiter) WaitRequest(req *http.Request) error {
	for _, l := range m.limiters {
		if rl, ok := l.(RequestLimiter); ok {
			if err := rl.WaitRequest(req); err != nil {
				return err
			}
		} else {
			if err := l.Wait(req.Context()); err != nil {
				return err
			}
		}
	}
	return nil
}

// RequestLimiter defines a rate limiter that can throttle based on the specific *http.Request
// (such as by host, scheme, or path).
type RequestLimiter interface {
	Limiter
	WaitRequest(req *http.Request) error
}

// HostRateLimiter manages separate TokenBucket rate limiters on a per-host basis.
type HostRateLimiter struct {
	mu             sync.RWMutex
	buckets        map[string]*TokenBucket
	requestsPerSec float64
	burst          int
}

// NewHostRateLimiter creates a rate limiter that applies the given rate and burst per destination host.
func NewHostRateLimiter(requestsPerSec float64, burst int) *HostRateLimiter {
	return &HostRateLimiter{
		buckets:        make(map[string]*TokenBucket),
		requestsPerSec: requestsPerSec,
		burst:          burst,
	}
}

// WaitRequest applies rate limiting specifically for the host in the given request.
func (hl *HostRateLimiter) WaitRequest(req *http.Request) error {
	if req == nil || req.URL == nil {
		return nil
	}
	host := req.URL.Hostname()
	if host == "" {
		host = req.Host
	}

	hl.mu.RLock()
	tb, ok := hl.buckets[host]
	hl.mu.RUnlock()

	if !ok {
		hl.mu.Lock()
		tb, ok = hl.buckets[host]
		if !ok {
			tb = NewTokenBucket(hl.requestsPerSec, hl.burst)
			hl.buckets[host] = tb
		}
		hl.mu.Unlock()
	}

	return tb.Wait(req.Context())
}

// Wait satisfies the Limiter interface for a global context.
func (hl *HostRateLimiter) Wait(ctx context.Context) error {
	return nil
}
