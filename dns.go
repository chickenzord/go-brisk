package brisk

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Resolver defines an interface for resolving hostnames to IP addresses.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DialContextWithResolver wraps an existing dialContext function, resolving the host
// using the provided Resolver before establishing the network connection.
func DialContextWithResolver(baseDial func(ctx context.Context, network, addr string) (net.Conn, error), resolver Resolver) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		return baseDial
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
			port = ""
		}

		// If it's already an IP address, skip custom DNS lookup
		if net.ParseIP(host) == nil {
			ips, lookupErr := resolver.LookupIPAddr(ctx, host)
			if lookupErr != nil {
				return nil, fmt.Errorf("brisk: custom dns resolution for %s failed: %w", host, lookupErr)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("brisk: custom dns returned no IP addresses for %s", host)
			}
			resolvedHost := ips[0].IP.String()
			if port != "" {
				addr = net.JoinHostPort(resolvedHost, port)
			} else {
				addr = resolvedHost
			}
		}

		return baseDial(ctx, network, addr)
	}
}

// NewCustomDNSResolver creates a *net.Resolver that directs queries to a specific DNS server (e.g. "1.1.1.1:53" or "8.8.8.8:53").
func NewCustomDNSResolver(dnsServerAddr string, timeout time.Duration) *net.Resolver {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: timeout,
			}
			return d.DialContext(ctx, "udp", dnsServerAddr)
		},
	}
}
