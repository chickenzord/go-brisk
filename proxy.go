package brisk

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type proxyNode struct {
	url           *url.URL
	failures      int
	unhealthyTill time.Time
}

// ProxyPoolConfig configures health tracking and fallback for rotating proxies.
type ProxyPoolConfig struct {
	// MaxFails is the number of consecutive errors before a proxy is temporarily marked unhealthy.
	// Default: 3.
	MaxFails int

	// Cooldown is the duration a proxy remains evicted before being retried.
	// Default: 30s.
	Cooldown time.Duration

	// FallbackDirect allows falling back to direct network connection if all proxies are unhealthy.
	// If false, an error is returned when all proxies are unhealthy.
	// Default: false.
	FallbackDirect bool
}

// DefaultProxyPoolConfig returns default proxy health policy settings.
func DefaultProxyPoolConfig() ProxyPoolConfig {
	return ProxyPoolConfig{
		MaxFails:       3,
		Cooldown:       30 * time.Second,
		FallbackDirect: false,
	}
}

// RotatingProxyPool cycles through a list of proxy URLs round-robin with health tracking.
type RotatingProxyPool struct {
	mu     sync.RWMutex
	nodes  []*proxyNode
	config ProxyPoolConfig
	index  uint64
}

// NewRotatingProxyPool creates a proxy pool with health tracking.
func NewRotatingProxyPool(rawURLs ...string) (*RotatingProxyPool, error) {
	return NewRotatingProxyPoolWithConfig(DefaultProxyPoolConfig(), rawURLs...)
}

// NewRotatingProxyPoolWithConfig creates a proxy pool with a custom health configuration.
func NewRotatingProxyPoolWithConfig(cfg ProxyPoolConfig, rawURLs ...string) (*RotatingProxyPool, error) {
	if cfg.MaxFails <= 0 {
		cfg.MaxFails = 3
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}

	var nodes []*proxyNode
	for _, raw := range rawURLs {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("brisk: invalid proxy URL %q: %w", raw, err)
		}
		nodes = append(nodes, &proxyNode{url: u})
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("brisk: proxy pool cannot be empty")
	}

	return &RotatingProxyPool{
		nodes:  nodes,
		config: cfg,
	}, nil
}

// Next selects the next healthy proxy URL in round-robin fashion.
// If all proxies are unhealthy, it either returns nil (direct connection fallback)
// or an error depending on FallbackDirect.
func (p *RotatingProxyPool) Next() (*url.URL, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	total := len(p.nodes)

	// Try to find a healthy node starting from next counter index
	for i := 0; i < total; i++ {
		idx := (atomic.AddUint64(&p.index, 1) - 1) % uint64(total)
		node := p.nodes[idx]

		// Check if in cooldown
		if !node.unhealthyTill.IsZero() {
			if now.After(node.unhealthyTill) {
				// Cooldown expired, restore node
				node.unhealthyTill = time.Time{}
				node.failures = 0
				return node.url, nil
			}
			// Node still unhealthy, check next
			continue
		}

		return node.url, nil
	}

	// All proxies currently unhealthy
	if p.config.FallbackDirect {
		return nil, nil // Nil URL signals direct connection to net.Dialer
	}

	return nil, errors.New("brisk: all proxies in pool are unhealthy and FallbackDirect is disabled")
}

// ReportFailure registers an error against the given proxy URL, incrementing its fail count
// and potentially placing it into cooldown.
func (p *RotatingProxyPool) ReportFailure(proxyURL *url.URL) {
	if proxyURL == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, node := range p.nodes {
		if node.url.String() == proxyURL.String() {
			node.failures++
			if node.failures >= p.config.MaxFails {
				node.unhealthyTill = time.Now().Add(p.config.Cooldown)
			}
			return
		}
	}
}

// ReportSuccess resets the consecutive error count for the given proxy.
func (p *RotatingProxyPool) ReportSuccess(proxyURL *url.URL) {
	if proxyURL == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, node := range p.nodes {
		if node.url.String() == proxyURL.String() {
			node.failures = 0
			node.unhealthyTill = time.Time{}
			return
		}
	}
}

// ProxyFunc returns a function compatible with http.Transport.Proxy.
func (p *RotatingProxyPool) ProxyFunc() func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		return p.Next()
	}
}

// ProxyPoolBuilder provides a fluent builder for configuring RotatingProxyPool.
type ProxyPoolBuilder struct {
	urls []string
	cfg  ProxyPoolConfig
}

// NewProxyPoolBuilder creates a builder for a rotating proxy pool.
func NewProxyPoolBuilder(proxyURLs ...string) *ProxyPoolBuilder {
	return &ProxyPoolBuilder{
		urls: proxyURLs,
		cfg:  DefaultProxyPoolConfig(),
	}
}

// MaxFails sets how many consecutive failures trigger proxy eviction.
func (b *ProxyPoolBuilder) MaxFails(n int) *ProxyPoolBuilder {
	b.cfg.MaxFails = n
	return b
}

// Cooldown sets how long an evicted proxy remains sidelined before being retried.
func (b *ProxyPoolBuilder) Cooldown(d time.Duration) *ProxyPoolBuilder {
	b.cfg.Cooldown = d
	return b
}

// FallbackDirect allows direct connection fallback when all proxies are unhealthy.
func (b *ProxyPoolBuilder) FallbackDirect(allow bool) *ProxyPoolBuilder {
	b.cfg.FallbackDirect = allow
	return b
}

// Build creates the configured RotatingProxyPool.
func (b *ProxyPoolBuilder) Build() (*RotatingProxyPool, error) {
	return NewRotatingProxyPoolWithConfig(b.cfg, b.urls...)
}
