package brisk

import (
	"context"
	"crypto/rand"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"time"
)

// RetryCondition determines whether an HTTP request should be retried based on
// the response or error.
type RetryCondition func(resp *http.Response, err error) bool

// BackoffCalculator computes the backoff duration for a given attempt (0-indexed).
type BackoffCalculator func(attempt int, resp *http.Response) time.Duration

// Sleeper abstracts waiting so tests can simulate time instantly and deterministically.
type Sleeper func(ctx context.Context, d time.Duration) error

// OnRetryHook is called before a retry wait is initiated, useful for logging, telemetry, or assertions.
type OnRetryHook func(req *http.Request, resp *http.Response, err error, attempt int, backoff time.Duration)

// RetryConfig configures the retry and backoff policy.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts (excluding the initial request).
	// Default: 3.
	MaxRetries int

	// InitialBackoff is the base wait duration for the first retry.
	// Default: 500ms.
	InitialBackoff time.Duration

	// MaxBackoff is the upper limit for backoff duration.
	// Default: 10s.
	MaxBackoff time.Duration

	// BackoffFactor is the multiplier for exponential backoff.
	// Default: 2.0.
	BackoffFactor float64

	// Jitter enables full randomized jitter [0, backoff] to prevent thundering herds.
	// Default: true.
	Jitter bool

	// RespectRetryAfter parses and respects Retry-After headers on 429 / 503 responses.
	// Default: true.
	RespectRetryAfter bool

	// MaxDrainBytes is the maximum bytes drained from the response body on retry.
	// Default: 4096 bytes.
	MaxDrainBytes int64

	// RetryCondition customizes when a request is retried.
	// If nil, DefaultRetryCondition is used.
	RetryCondition RetryCondition

	// BackoffCalculator customizes backoff calculation.
	// If nil, DefaultBackoffCalculator is used.
	BackoffCalculator BackoffCalculator

	// Sleeper pauses execution during backoff.
	// Defaults to standard time.After with context awareness.
	Sleeper Sleeper

	// OnRetry is invoked right before each retry sleep.
	OnRetry OnRetryHook
}

// DefaultRetryConfig returns sensible default settings for scraping and resilient HTTP calls.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    500 * time.Millisecond,
		MaxBackoff:        10 * time.Second,
		BackoffFactor:     2.0,
		Jitter:            true,
		RespectRetryAfter: true,
		MaxDrainBytes:     4096,
	}
}

// RetryBuilder provides a fluent builder to configure retry and backoff strategies.
type RetryBuilder struct {
	cfg        RetryConfig
	statusList []int
}

// NewRetryBuilder creates a RetryBuilder pre-initialized with sensible defaults.
func NewRetryBuilder() *RetryBuilder {
	return &RetryBuilder{
		cfg: DefaultRetryConfig(),
	}
}

// MaxRetries sets the maximum number of retry attempts.
func (rb *RetryBuilder) MaxRetries(n int) *RetryBuilder {
	rb.cfg.MaxRetries = n
	return rb
}

// InitialBackoff sets the base wait duration for the first retry.
func (rb *RetryBuilder) InitialBackoff(d time.Duration) *RetryBuilder {
	rb.cfg.InitialBackoff = d
	return rb
}

// MaxBackoff sets the maximum wait duration cap.
func (rb *RetryBuilder) MaxBackoff(d time.Duration) *RetryBuilder {
	rb.cfg.MaxBackoff = d
	return rb
}

// BackoffFactor sets the multiplier for exponential backoff.
func (rb *RetryBuilder) BackoffFactor(factor float64) *RetryBuilder {
	rb.cfg.BackoffFactor = factor
	return rb
}

// Jitter enables or disables randomized jitter.
func (rb *RetryBuilder) Jitter(enable bool) *RetryBuilder {
	rb.cfg.Jitter = enable
	return rb
}

// RespectRetryAfter controls whether Retry-After headers are parsed and respected.
func (rb *RetryBuilder) RespectRetryAfter(enable bool) *RetryBuilder {
	rb.cfg.RespectRetryAfter = enable
	return rb
}

// MaxDrainBytes sets how many bytes of the unread body to drain to reuse TCP connections.
func (rb *RetryBuilder) MaxDrainBytes(n int64) *RetryBuilder {
	rb.cfg.MaxDrainBytes = n
	return rb
}

// When sets a custom condition function to decide when to retry.
func (rb *RetryBuilder) When(condition RetryCondition) *RetryBuilder {
	rb.cfg.RetryCondition = condition
	return rb
}

// WhenStatus retries only when the response status code matches one of the specified codes.
func (rb *RetryBuilder) WhenStatus(statuses ...int) *RetryBuilder {
	rb.statusList = append(rb.statusList, statuses...)
	targetStatuses := make([]int, len(rb.statusList))
	copy(targetStatuses, rb.statusList)

	rb.cfg.RetryCondition = func(resp *http.Response, err error) bool {
		if err != nil {
			return true // Network errors are retried
		}
		if resp == nil {
			return false
		}
		for _, s := range targetStatuses {
			if resp.StatusCode == s {
				return true
			}
		}
		return false
	}
	return rb
}

// WhenIdempotent restricts retries to idempotent HTTP methods (GET, HEAD, OPTIONS, PUT, DELETE).
func (rb *RetryBuilder) WhenIdempotent() *RetryBuilder {
	existing := rb.cfg.RetryCondition
	if existing == nil {
		existing = DefaultRetryCondition
	}
	rb.cfg.RetryCondition = func(resp *http.Response, err error) bool {
		if !existing(resp, err) {
			return false
		}
		if resp != nil && resp.Request != nil {
			switch resp.Request.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
				return true
			default:
				return false
			}
		}
		return true
	}
	return rb
}

// WhenCloudflare retries common Cloudflare rate-limiting, challenge, and origin error codes
// (429, 403, 520, 521, 522, 523, 524).
func (rb *RetryBuilder) WhenCloudflare() *RetryBuilder {
	return rb.WhenStatus(
		http.StatusTooManyRequests,
		http.StatusForbidden,
		520, 521, 522, 523, 524,
	)
}

// BackoffCalculator sets a custom backoff duration calculator.
func (rb *RetryBuilder) BackoffCalculator(calc BackoffCalculator) *RetryBuilder {
	rb.cfg.BackoffCalculator = calc
	return rb
}

// Sleeper sets a custom sleeper, allowing instant virtual time in tests.
func (rb *RetryBuilder) Sleeper(s Sleeper) *RetryBuilder {
	rb.cfg.Sleeper = s
	return rb
}

// OnRetry sets a notification callback invoked before each retry wait.
func (rb *RetryBuilder) OnRetry(hook OnRetryHook) *RetryBuilder {
	rb.cfg.OnRetry = hook
	return rb
}

// Config returns the finalized RetryConfig.
func (rb *RetryBuilder) Config() RetryConfig {
	return rb.cfg
}

// DefaultRetryCondition retries on network/timeout errors, 429 Too Many Requests,
// and 5xx server errors (500, 502, 503, 504).
func DefaultRetryCondition(resp *http.Response, err error) bool {
	if err != nil {
		var netErr net.Error
		if errorsAs(err, &netErr) {
			return netErr.Timeout()
		}
		return true
	}

	if resp == nil {
		return false
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests: // 429
		return true
	case http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:       // 504
		return true
	default:
		return false
	}
}

// IdempotentRetryCondition retries only for idempotent HTTP methods (GET, HEAD, OPTIONS, PUT, DELETE).
func IdempotentRetryCondition(resp *http.Response, err error) bool {
	if !DefaultRetryCondition(resp, err) {
		return false
	}
	if resp != nil && resp.Request != nil {
		switch resp.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
			return true
		default:
			return false
		}
	}
	return true
}

func isNetTimeoutOrTemporary(err error) bool {
	var netErr net.Error
	if errorsAs(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

func errorsAs(err error, target any) bool {
	if err == nil || target == nil {
		return false
	}
	t, ok := target.(*net.Error)
	if !ok {
		return false
	}
	if ne, ok := err.(net.Error); ok {
		*t = ne
		return true
	}
	return false
}

// ParseRetryAfter parses a Retry-After header into a time.Duration.
// Supports both delta-seconds (e.g. "120") and HTTP-date (RFC1123 / RFC850 / ANSIC).
func ParseRetryAfter(headerVal string) (time.Duration, bool) {
	if headerVal == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(headerVal); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}

	dateFormats := []string{
		http.TimeFormat,
		time.RFC850,
		time.ANSIC,
	}
	for _, layout := range dateFormats {
		if t, err := time.Parse(layout, headerVal); err == nil {
			duration := time.Until(t)
			if duration < 0 {
				return 0, true
			}
			return duration, true
		}
	}

	return 0, false
}

// CalculateBackoff computes exponential backoff with optional full jitter and Retry-After support.
func (cfg RetryConfig) CalculateBackoff(attempt int, resp *http.Response) time.Duration {
	if cfg.BackoffCalculator != nil {
		return cfg.BackoffCalculator(attempt, resp)
	}

	if cfg.RespectRetryAfter && resp != nil {
		if d, ok := ParseRetryAfter(resp.Header.Get("Retry-After")); ok {
			if cfg.MaxBackoff > 0 && d > cfg.MaxBackoff {
				return cfg.MaxBackoff
			}
			return d
		}
	}

	base := cfg.InitialBackoff
	if base <= 0 {
		base = 500 * time.Millisecond
	}

	factor := cfg.BackoffFactor
	if factor <= 1.0 {
		factor = 2.0
	}

	multiplier := math.Pow(factor, float64(attempt))
	backoffNano := float64(base) * multiplier

	var backoff time.Duration
	if backoffNano > float64(math.MaxInt64) {
		backoff = cfg.MaxBackoff
	} else {
		backoff = time.Duration(backoffNano)
	}

	if cfg.MaxBackoff > 0 && backoff > cfg.MaxBackoff {
		backoff = cfg.MaxBackoff
	}

	if cfg.Jitter && backoff > 0 {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(backoff)))
		if err == nil {
			backoff = time.Duration(n.Int64())
		}
	}

	return backoff
}

// defaultSleep pauses execution for duration d or until ctx is done.
func defaultSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// retryRoundTripper is a composable RoundTripper that wraps any transport with automatic retry and backoff.
type retryRoundTripper struct {
	next   http.RoundTripper
	config RetryConfig
}

// NewRetryRoundTripper creates an http.RoundTripper that retries failed requests according to cfg.
func NewRetryRoundTripper(next http.RoundTripper, cfg RetryConfig) http.RoundTripper {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 10 * time.Second
	}
	if cfg.BackoffFactor <= 1.0 {
		cfg.BackoffFactor = 2.0
	}
	if cfg.MaxDrainBytes <= 0 {
		cfg.MaxDrainBytes = 4096
	}
	if cfg.Sleeper == nil {
		cfg.Sleeper = defaultSleep
	}
	return &retryRoundTripper{
		next:   next,
		config: cfg,
	}
}

// RoundTrip executes the request with backoff and retry.
func (rt *retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	retryCond := rt.config.RetryCondition
	if retryCond == nil {
		retryCond = DefaultRetryCondition
	}

	sleeper := rt.config.Sleeper
	if sleeper == nil {
		sleeper = defaultSleep
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= rt.config.MaxRetries; attempt++ {
		if attempt > 0 && req.Body != nil {
			if req.GetBody == nil {
				return resp, err
			}
			newBody, getErr := req.GetBody()
			if getErr != nil {
				return resp, getErr
			}
			req.Body = newBody
		}

		resp, err = rt.next.RoundTrip(req)

		shouldRetry := retryCond(resp, err)
		if !shouldRetry || attempt == rt.config.MaxRetries {
			return resp, err
		}

		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, rt.config.MaxDrainBytes))
			_ = resp.Body.Close()
		}

		backoff := rt.config.CalculateBackoff(attempt, resp)

		if rt.config.OnRetry != nil {
			rt.config.OnRetry(req, resp, err, attempt+1, backoff)
		}

		if sleepErr := sleeper(req.Context(), backoff); sleepErr != nil {
			return nil, sleepErr
		}
	}

	return resp, err
}
