package brisk

import (
	"time"
)

// PoolConfig holds connection pool configuration for the HTTP transports.
type PoolConfig struct {
	// MaxIdleConns controls the maximum number of idle (keep-alive)
	// connections across all hosts.
	MaxIdleConns int

	// MaxIdleConnsPerHost controls the maximum idle (keep-alive)
	// connections to keep per-host.
	MaxIdleConnsPerHost int

	// MaxConnsPerHost optionally limits the total number of
	// connections per host, including connections in the dialing,
	// active, and idle states. Zero means no limit.
	MaxConnsPerHost int

	// IdleConnTimeout is the maximum amount of time an idle
	// (keep-alive) connection will remain idle before closing itself.
	IdleConnTimeout time.Duration
}

// DefaultPoolConfig returns sensible defaults for high-performance scraping
// and browsing without exhausting server sockets.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		MaxConnsPerHost:     0, // 0 = unlimited active connections
		IdleConnTimeout:     90 * time.Second,
	}
}
