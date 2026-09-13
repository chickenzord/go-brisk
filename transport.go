package brisk

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// Transport is an http.RoundTripper that mimics modern browser TLS fingerprints (uTLS)
// and handles HTTP/2 framing with persistent connection pooling.
type Transport struct {
	h2Tr           *http2.Transport
	h1Tr           *http.Transport
	limiter        Limiter
	tlsProfile     TLSProfile
	tlsProfileFunc TLSProfileSelector
	dialTimeout    time.Duration
	proxyFunc      func(*http.Request) (*url.URL, error)
	disableH2      bool
}

// Config specifies the low-level settings to instantiate a Transport.
type Config struct {
	TLSProfile         TLSProfile
	TLSProfileFunc     TLSProfileSelector
	InsecureSkipVerify bool
	RootCAs            *x509.CertPool
	DialTimeout        time.Duration
	DialContext        func(ctx context.Context, network, addr string) (net.Conn, error)
	PoolConfig         PoolConfig
	Proxy              func(*http.Request) (*url.URL, error)
	Limiter            Limiter
	DisableHTTP2       bool
}

// NewTransport constructs a new brisk.Transport with the provided configuration.
func NewTransport(cfg Config) (*Transport, error) {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.TLSProfile.id.Client == "" && cfg.TLSProfileFunc == nil {
		cfg.TLSProfile = DefaultTLSProfile
	}
	if cfg.Proxy == nil {
		cfg.Proxy = http.ProxyFromEnvironment
	}

	dialContext := cfg.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		}
		dialContext = dialer.DialContext
	}

	// 1. Configure HTTP/1.1 Transport
	h1Tr := &http.Transport{
		Proxy:                 cfg.Proxy,
		DialContext:           dialContext,
		MaxIdleConns:          cfg.PoolConfig.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.PoolConfig.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.PoolConfig.MaxConnsPerHost,
		IdleConnTimeout:       cfg.PoolConfig.IdleConnTimeout,
		TLSHandshakeTimeout:   cfg.DialTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     false,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			RootCAs:            cfg.RootCAs,
		},
	}

	// 2. Configure HTTP/2 Transport with uTLS for https:// requests (if not disabled)
	var h2Tr *http2.Transport
	if !cfg.DisableHTTP2 {
		h2Tr = &http2.Transport{
			IdleConnTimeout: cfg.PoolConfig.IdleConnTimeout,
		}

		h2Tr.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				host = addr
				port = "443"
				addr = net.JoinHostPort(host, port)
			}

			rawConn, err := dialDestination(ctx, dialContext, cfg.Proxy, addr)
			if err != nil {
				return nil, err
			}

			tlsConfig := &utls.Config{
				ServerName:         host,
				InsecureSkipVerify: cfg.InsecureSkipVerify,
				RootCAs:            cfg.RootCAs,
				NextProtos:         []string{"h2", "http/1.1"},
			}

			activeProfile := cfg.TLSProfile
			if cfg.TLSProfileFunc != nil {
				activeProfile = cfg.TLSProfileFunc()
			}

			uConn := utls.UClient(rawConn, tlsConfig, activeProfile.id)
			if hsErr := uConn.HandshakeContext(ctx); hsErr != nil {
				_ = rawConn.Close()
				return nil, fmt.Errorf("brisk: utls handshake failed with %s: %w", host, hsErr)
			}

			return uConn, nil
		}
	}

	return &Transport{
		h2Tr:           h2Tr,
		h1Tr:           h1Tr,
		limiter:        cfg.Limiter,
		tlsProfile:     cfg.TLSProfile,
		tlsProfileFunc: cfg.TLSProfileFunc,
		dialTimeout:    cfg.DialTimeout,
		proxyFunc:      cfg.Proxy,
		disableH2:      cfg.DisableHTTP2,
	}, nil
}

// dialDestination handles direct dialing or HTTP CONNECT proxying for HTTPS requests.
func dialDestination(ctx context.Context, dialContext func(context.Context, string, string) (net.Conn, error), proxyFunc func(*http.Request) (*url.URL, error), targetAddr string) (net.Conn, error) {
	dummyReq := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   targetAddr,
		},
	}

	proxyURL, err := proxyFunc(dummyReq)
	if err != nil {
		return nil, fmt.Errorf("brisk: resolving proxy failed: %w", err)
	}

	if proxyURL == nil {
		return dialContext(ctx, "tcp", targetAddr)
	}

	proxyAddr := proxyURL.Host
	if !strings.Contains(proxyAddr, ":") {
		if proxyURL.Scheme == "https" {
			proxyAddr += ":443"
		} else {
			proxyAddr += ":80"
		}
	}

	proxyConn, err := dialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("brisk: connecting to proxy %s failed: %w", proxyAddr, err)
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", targetAddr, targetAddr)
	if proxyURL.User != nil {
		auth := proxyURL.User.String()
		basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(auth))
		connectReq += fmt.Sprintf("Proxy-Authorization: %s\r\n", basicAuth)
	}
	connectReq += "\r\n"

	if _, err := proxyConn.Write([]byte(connectReq)); err != nil {
		_ = proxyConn.Close()
		return nil, fmt.Errorf("brisk: sending CONNECT to proxy failed: %w", err)
	}

	br := bufio.NewReader(proxyConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = proxyConn.Close()
		return nil, fmt.Errorf("brisk: reading CONNECT response from proxy failed: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = proxyConn.Close()
		return nil, fmt.Errorf("brisk: proxy tunnel to %s refused with status %d", targetAddr, resp.StatusCode)
	}

	return proxyConn, nil
}

// RoundTrip fulfills http.RoundTripper, applying rate limiting and routing requests.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("brisk: nil http.Request or nil URL")
	}

	if t.limiter != nil {
		if rl, ok := t.limiter.(RequestLimiter); ok {
			if err := rl.WaitRequest(req); err != nil {
				return nil, fmt.Errorf("brisk: rate limit wait failed: %w", err)
			}
		} else {
			if err := t.limiter.Wait(req.Context()); err != nil {
				return nil, fmt.Errorf("brisk: rate limit wait failed: %w", err)
			}
		}
	}

	if req.URL.Scheme == "https" && !t.disableH2 && t.h2Tr != nil {
		return t.h2Tr.RoundTrip(req)
	}
	return t.h1Tr.RoundTrip(req)
}

// CloseIdleConnections closes all idle keep-alive connections on both HTTP/1 and HTTP/2 pools.
func (t *Transport) CloseIdleConnections() {
	if t.h2Tr != nil {
		t.h2Tr.CloseIdleConnections()
	}
	if t.h1Tr != nil {
		t.h1Tr.CloseIdleConnections()
	}
}
