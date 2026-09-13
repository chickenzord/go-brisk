package brisk

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Builder provides a fluent builder pattern to configure and construct an *http.Client
// powered by brisk.Transport.
type Builder struct {
	poolConfig         PoolConfig
	tlsProfile         utls.ClientHelloID
	insecureSkipVerify bool
	timeout            time.Duration
	dialTimeout        time.Duration
	proxyFunc          func(*http.Request) (*url.URL, error)
	headers            map[string]string
	jar                http.CookieJar
	limiters           []Limiter
	err                error
}

// NewBuilder initializes a new Builder with default, sensible settings.
func NewBuilder() *Builder {
	return &Builder{
		poolConfig:  DefaultPoolConfig(),
		tlsProfile:  utls.HelloChrome_120,
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

// WithTLSProfile sets the specific uTLS ClientHello fingerprint profile to emulate.
func (b *Builder) WithTLSProfile(id utls.ClientHelloID) *Builder {
	b.tlsProfile = id
	return b
}

// WithInsecureSkipVerify controls whether to skip TLS certificate verification.
func (b *Builder) WithInsecureSkipVerify(skip bool) *Builder {
	b.insecureSkipVerify = skip
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

// WithCookieJar assigns an http.CookieJar to manage session cookies.
func (b *Builder) WithCookieJar(jar http.CookieJar) *Builder {
	b.jar = jar
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

// WithBrowserHeaders injects common standard browser headers (User-Agent, Sec-CH-UA, Accept, etc.).
func (b *Builder) WithBrowserHeaders() *Builder {
	return b.WithHeaders(DefaultBrowserHeaders())
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

	return NewTransport(Config{
		TLSProfile:         b.tlsProfile,
		InsecureSkipVerify: b.insecureSkipVerify,
		DialTimeout:        b.dialTimeout,
		PoolConfig:         b.poolConfig,
		Proxy:              b.proxyFunc,
		Limiter:            combinedLimiter,
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
		roundTripper = newHeaderRoundTripper(transport, b.headers)
	}

	return &http.Client{
		Transport: roundTripper,
		Timeout:   b.timeout,
		Jar:       b.jar,
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
