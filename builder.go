package brisk

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Middleware wraps an http.RoundTripper to inject custom functionality.
type Middleware func(http.RoundTripper) http.RoundTripper

// Builder provides a fluent builder pattern to configure and construct an *http.Client
// powered by brisk.Transport.
type Builder struct {
	poolConfig          PoolConfig
	tlsProfile          TLSProfile
	tlsProfileFunc      TLSProfileSelector
	insecureSkipVerify  bool
	rootCAs             *x509.CertPool
	timeout             time.Duration
	dialTimeout         time.Duration
	dialContext         func(ctx context.Context, network, addr string) (net.Conn, error)
	resolver            Resolver
	proxyFunc           func(*http.Request) (*url.URL, error)
	headers             map[string]string
	jar                 http.CookieJar
	checkRedirect       func(req *http.Request, via []*http.Request) error
	limiters            []Limiter
	retryConfig         *RetryConfig
	retryEnabled        bool
	singleflightConfig  *SingleflightConfig
	singleflightEnabled bool
	disableHTTP2        bool
	middlewares         []Middleware
	err                 error
}

// NewBuilder initializes a new Builder with default, sensible settings.
func NewBuilder() *Builder {
	return &Builder{
		poolConfig:  DefaultPoolConfig(),
		tlsProfile:  DefaultTLSProfile,
		timeout:     30 * time.Second,
		dialTimeout: 10 * time.Second,
		proxyFunc:   http.ProxyFromEnvironment,
		headers:     make(map[string]string),
	}
}

// New creates a ready-to-use *http.Client with default brisk settings.
func New() (*http.Client, error) {
	return NewBuilder().Build()
}

// MustNew creates a ready-to-use *http.Client or panics on initialization failure.
func MustNew() *http.Client {
	return NewBuilder().MustBuild()
}

// WithPoolConfig replaces the entire connection pool configuration.
func (b *Builder) WithPoolConfig(cfg PoolConfig) *Builder {
	b.poolConfig = cfg
	return b
}

// WithMaxIdleConns configures the overall maximum idle connections.
func (b *Builder) WithMaxIdleConns(n int) *Builder {
	b.poolConfig.MaxIdleConns = n
	return b
}

// WithMaxIdleConnsPerHost configures the maximum idle connections per host origin.
func (b *Builder) WithMaxIdleConnsPerHost(n int) *Builder {
	b.poolConfig.MaxIdleConnsPerHost = n
	return b
}

// WithMaxConnsPerHost limits the total active connections per host (0 = unlimited).
func (b *Builder) WithMaxConnsPerHost(n int) *Builder {
	b.poolConfig.MaxConnsPerHost = n
	return b
}

// WithIdleConnTimeout sets the idle connection keep-alive timeout.
func (b *Builder) WithIdleConnTimeout(d time.Duration) *Builder {
	b.poolConfig.IdleConnTimeout = d
	return b
}

// WithTLSProfile sets a specific static TLS fingerprint profile.
func (b *Builder) WithTLSProfile(profile TLSProfile) *Builder {
	b.tlsProfile = profile
	b.tlsProfileFunc = nil
	return b
}

// WithTLSProfileFunc configures dynamic per-request TLS fingerprint selection.
func (b *Builder) WithTLSProfileFunc(fn TLSProfileSelector) *Builder {
	b.tlsProfileFunc = fn
	return b
}

// WithRandomTLSProfile enables randomized ClientHello rotation among standard browser profiles.
func (b *Builder) WithRandomTLSProfile() *Builder {
	b.tlsProfileFunc = NewProfileRotator()
	return b
}

// WithTLSProfiles enables randomized ClientHello rotation among the specifically provided profiles.
func (b *Builder) WithTLSProfiles(profiles []TLSProfile) *Builder {
	b.tlsProfileFunc = NewProfileRotator(profiles...)
	return b
}

// WithInsecureSkipVerify controls whether to skip TLS certificate verification.
func (b *Builder) WithInsecureSkipVerify(skip bool) *Builder {
	b.insecureSkipVerify = skip
	return b
}

// WithRootCAs sets a custom certificate pool for TLS verification.
func (b *Builder) WithRootCAs(rootCAs *x509.CertPool) *Builder {
	b.rootCAs = rootCAs
	return b
}

// WithTimeout sets the overall request timeout on the resulting *http.Client.
func (b *Builder) WithTimeout(d time.Duration) *Builder {
	b.timeout = d
	return b
}

// WithDialTimeout sets the network dial timeout for new connections.
func (b *Builder) WithDialTimeout(d time.Duration) *Builder {
	b.dialTimeout = d
	return b
}

// WithDialContext customizes the dialer function, ideal for in-memory virtual testing or custom sockets.
func (b *Builder) WithDialContext(fn func(ctx context.Context, network, addr string) (net.Conn, error)) *Builder {
	b.dialContext = fn
	return b
}

// WithResolver configures a custom DNS Resolver (such as custom DoH or local DNS stubs).
func (b *Builder) WithResolver(resolver Resolver) *Builder {
	b.resolver = resolver
	return b
}

// WithDNS configures a specific remote DNS server address (e.g. "1.1.1.1:53" or "8.8.8.8:53").
func (b *Builder) WithDNS(dnsServerAddr string) *Builder {
	b.resolver = NewCustomDNSResolver(dnsServerAddr, 5*time.Second)
	return b
}

// WithProxy configures an explicit HTTP, HTTPS, or SOCKS5 proxy URL string.
func (b *Builder) WithProxy(rawURL string) *Builder {
	if rawURL == "" {
		b.proxyFunc = nil
		return b
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		b.err = fmt.Errorf("brisk: invalid proxy URL %q: %w", rawURL, err)
		return b
	}
	b.proxyFunc = http.ProxyURL(parsed)
	return b
}

// WithProxyFunc configures a dynamic proxy selector function.
func (b *Builder) WithProxyFunc(fn func(*http.Request) (*url.URL, error)) *Builder {
	b.proxyFunc = fn
	return b
}

// WithProxyPool configures a round-robin rotating proxy pool with default health settings.
func (b *Builder) WithProxyPool(proxyURLs []string) *Builder {
	return b.WithProxyPoolBuilder(proxyURLs, nil)
}

// WithProxyPoolBuilder configures a round-robin rotating proxy pool with custom health settings.
func (b *Builder) WithProxyPoolBuilder(proxyURLs []string, fn func(*ProxyPoolBuilder)) *Builder {
	pb := NewProxyPoolBuilder(proxyURLs...)
	if fn != nil {
		fn(pb)
	}
	pool, err := pb.Build()
	if err != nil {
		b.err = err
		return b
	}
	b.proxyFunc = pool.ProxyFunc()
	return b
}

// WithCookieJar assigns an http.CookieJar to manage session cookies.
func (b *Builder) WithCookieJar(jar http.CookieJar) *Builder {
	b.jar = jar
	return b
}

// WithCheckRedirect configures the redirect policy for the http.Client.
func (b *Builder) WithCheckRedirect(fn func(req *http.Request, via []*http.Request) error) *Builder {
	b.checkRedirect = fn
	return b
}

// WithDisableRedirects disables following HTTP redirects (301/302/303/307/308).
func (b *Builder) WithDisableRedirects() *Builder {
	return b.WithCheckRedirect(func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	})
}

// WithMaxRedirects limits the maximum number of consecutive redirects before failing.
func (b *Builder) WithMaxRedirects(max int) *Builder {
	return b.WithCheckRedirect(func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return fmt.Errorf("stopped after %d redirects", max)
		}
		return nil
	})
}

// WithDisableHTTP2 forces HTTP/1.1 and disables HTTP/2 framing.
func (b *Builder) WithDisableHTTP2(disable bool) *Builder {
	b.disableHTTP2 = disable
	return b
}

// WithHeader adds a default header to all requests if not explicitly set by the caller.
func (b *Builder) WithHeader(key, value string) *Builder {
	b.headers[key] = value
	return b
}

// WithHeaders merges a map of default headers to all requests.
func (b *Builder) WithHeaders(headers map[string]string) *Builder {
	for k, v := range headers {
		b.headers[k] = v
	}
	return b
}

// WithHeadersFunc injects headers generated by a header provider function (e.g. DesktopChromeHeaders, MobileChromeHeaders).
func (b *Builder) WithHeadersFunc(fn func() map[string]string) *Builder {
	if fn != nil {
		b.WithHeaders(fn())
	}
	return b
}

// WithUserAgent sets a default User-Agent string.
func (b *Builder) WithUserAgent(ua string) *Builder {
	return b.WithHeader("User-Agent", ua)
}

// WithRateLimit adds a token bucket rate limiter to throttle requests per second.
func (b *Builder) WithRateLimit(requestsPerSec float64, burst int) *Builder {
	b.limiters = append(b.limiters, NewTokenBucket(requestsPerSec, burst))
	return b
}

// WithHostRateLimit adds a per-host token bucket rate limiter.
func (b *Builder) WithHostRateLimit(requestsPerSec float64, burst int) *Builder {
	b.limiters = append(b.limiters, NewHostRateLimiter(requestsPerSec, burst))
	return b
}

// WithRequestDelay adds a randomized artificial sleep duration within [min, max] between requests.
func (b *Builder) WithRequestDelay(min, max time.Duration) *Builder {
	b.limiters = append(b.limiters, NewDelayJitter(min, max))
	return b
}

// WithLimiter adds a custom Limiter implementation.
func (b *Builder) WithLimiter(limiter Limiter) *Builder {
	if limiter != nil {
		b.limiters = append(b.limiters, limiter)
	}
	return b
}

// WithRetry enables automatic retry and backoff with default configuration (3 retries, exponential backoff with jitter).
func (b *Builder) WithRetry() *Builder {
	b.retryEnabled = true
	if b.retryConfig == nil {
		cfg := DefaultRetryConfig()
		b.retryConfig = &cfg
	}
	return b
}

// WithRetryBuilder enables retries configured via a fluent *RetryBuilder closure.
func (b *Builder) WithRetryBuilder(fn func(*RetryBuilder)) *Builder {
	b.WithRetry()
	rb := NewRetryBuilder()
	if b.retryConfig != nil {
		rb.cfg = *b.retryConfig
	}
	if fn != nil {
		fn(rb)
	}
	cfg := rb.Config()
	b.retryConfig = &cfg
	return b
}

// WithRetryConditionFunc enables retries with default backoff settings and a custom RetryCondition function.
func (b *Builder) WithRetryConditionFunc(condition RetryCondition) *Builder {
	return b.WithRetryBuilder(func(r *RetryBuilder) {
		r.When(condition)
	})
}

// WithRetryConfig configures custom automatic retry and backoff parameters directly.
func (b *Builder) WithRetryConfig(cfg RetryConfig) *Builder {
	b.retryEnabled = true
	b.retryConfig = &cfg
	return b
}

// WithSingleflight enables in-flight request deduplication for idempotent read methods (GET/HEAD).
func (b *Builder) WithSingleflight() *Builder {
	b.singleflightEnabled = true
	if b.singleflightConfig == nil {
		b.singleflightConfig = &SingleflightConfig{}
	}
	return b
}

// WithSingleflightBuilder enables in-flight request deduplication configured via a *SingleflightBuilder closure.
func (b *Builder) WithSingleflightBuilder(fn func(*SingleflightBuilder)) *Builder {
	b.WithSingleflight()
	sb := NewSingleflightBuilder()
	if b.singleflightConfig != nil {
		sb.cfg = *b.singleflightConfig
	}
	if fn != nil {
		fn(sb)
	}
	cfg := sb.Config()
	b.singleflightConfig = &cfg
	return b
}

// WithSingleflightKeyFunc enables in-flight request deduplication using a custom key generation function.
func (b *Builder) WithSingleflightKeyFunc(keyFunc SingleflightKeyFunc) *Builder {
	return b.WithSingleflightBuilder(func(s *SingleflightBuilder) {
		s.KeyFunc(keyFunc)
	})
}

// WithSingleflightConfig configures custom request deduplication parameters directly.
func (b *Builder) WithSingleflightConfig(cfg SingleflightConfig) *Builder {
	b.singleflightEnabled = true
	b.singleflightConfig = &cfg
	return b
}

// WithMiddleware appends one or more custom RoundTripper middleware wrappers.
func (b *Builder) WithMiddleware(mw ...Middleware) *Builder {
	for _, m := range mw {
		if m != nil {
			b.middlewares = append(b.middlewares, m)
		}
	}
	return b
}

// BuildTransport constructs and returns the underlying *brisk.Transport.
func (b *Builder) BuildTransport() (*Transport, error) {
	if b.err != nil {
		return nil, b.err
	}

	var combinedLimiter Limiter
	if len(b.limiters) == 1 {
		combinedLimiter = b.limiters[0]
	} else if len(b.limiters) > 1 {
		combinedLimiter = NewMultiLimiter(b.limiters...)
	}

	dialContext := b.dialContext
	if b.resolver != nil {
		baseDial := dialContext
		if baseDial == nil {
			dialer := &net.Dialer{
				Timeout:   b.dialTimeout,
				KeepAlive: 30 * time.Second,
			}
			baseDial = dialer.DialContext
		}
		dialContext = DialContextWithResolver(baseDial, b.resolver)
	}

	return NewTransport(Config{
		TLSProfile:         b.tlsProfile,
		TLSProfileFunc:     b.tlsProfileFunc,
		InsecureSkipVerify: b.insecureSkipVerify,
		RootCAs:            b.rootCAs,
		DialTimeout:        b.dialTimeout,
		DialContext:        dialContext,
		PoolConfig:         b.poolConfig,
		Proxy:              b.proxyFunc,
		Limiter:            combinedLimiter,
		DisableHTTP2:       b.disableHTTP2,
	})
}

// Build constructs and returns a standard library *http.Client configured with brisk.Transport.
func (b *Builder) Build() (*http.Client, error) {
	transport, err := b.BuildTransport()
	if err != nil {
		return nil, err
	}

	var roundTripper http.RoundTripper = transport

	if len(b.headers) > 0 {
		roundTripper = newHeaderRoundTripper(roundTripper, b.headers)
	}

	if b.singleflightEnabled {
		cfg := SingleflightConfig{}
		if b.singleflightConfig != nil {
			cfg = *b.singleflightConfig
		}
		roundTripper = NewSingleflightRoundTripper(roundTripper, cfg)
	}

	if b.retryEnabled {
		cfg := DefaultRetryConfig()
		if b.retryConfig != nil {
			cfg = *b.retryConfig
		}
		roundTripper = NewRetryRoundTripper(roundTripper, cfg)
	}

	for i := len(b.middlewares) - 1; i >= 0; i-- {
		roundTripper = b.middlewares[i](roundTripper)
	}

	return &http.Client{
		Transport:     roundTripper,
		Timeout:       b.timeout,
		Jar:           b.jar,
		CheckRedirect: b.checkRedirect,
	}, nil
}

// MustBuild constructs the *http.Client or panics if configuration fails.
func (b *Builder) MustBuild() *http.Client {
	client, err := b.Build()
	if err != nil {
		panic(err)
	}
	return client
}
