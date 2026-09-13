# brisk

Fast, stealthy, and lightweight Go HTTP transport with browser TLS fingerprinting (uTLS), HTTP/2 multiplexing, per-host connection pooling, and zero-dependency rate limiting.

Designed as a drop-in replacement for Go's standard library `*http.Client`.

---

## 🚀 Features

- **Standard `*http.Client` Drop-In**: Seamlessly works with any Go library or codebase that accepts `*http.Client` or `http.RoundTripper`.
- **TLS Anti-Fingerprinting (uTLS)**: Mimics real browser ClientHello signatures (default: Chrome 120) with ALPN `h2` to bypass JA3/JA4 TLS bot detection.
- **HTTP/2 Multiplexing**: Native HTTP/2 framing and stream multiplexing over uTLS connections.
- **Configurable Connection Pooling**: Per-host keep-alive pooling with customizable idle connection limits, total active connection limits, and timeouts.
- **Opt-in Header Injection**: Optionally injects realistic browser headers (`User-Agent`, `Sec-CH-UA`, `Accept-Language`, etc.) without overwriting explicit request headers.
- **Zero-Dependency Rate Limiting**: Built-in token bucket rate limiter and randomized delay jitter using only Go's standard library (`time` and `sync`).
- **Proxy Support**: Supports HTTP, HTTPS, and SOCKS5 proxies (`WithProxy`) with automatic fallback to environment variables (`http.ProxyFromEnvironment`).
- **Minimal Dependencies & Broad Compatibility**:
  - Only requires `github.com/refraction-networking/utls` and `golang.org/x/net/http2`.
  - Target minimum Go version: `1.21` (no generics).

---

## 📦 Installation

```bash
go get github.com/chickenzord/go-brisk
```

---

## 💻 Quick Start

### Basic Usage with Defaults

```go
package main

import (
	"fmt"
	"io"
	"log"

	"github.com/chickenzord/go-brisk"
)

func main() {
	// Creates a standard *http.Client with Chrome TLS fingerprinting
	client, err := brisk.New()
	if err != nil {
		log.Fatal(err)
	}

	resp, err := client.Get("https://httpbin.org/get")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Status: %s, Protocol: %s, Length: %d\n", resp.Status, resp.Proto, len(body))
}
```

---

## 🛠️ Advanced Configuration (Builder Pattern)

```go
package main

import (
	"time"

	"github.com/chickenzord/go-brisk"
	utls "github.com/refraction-networking/utls"
)

func main() {
	client, err := brisk.NewBuilder().
		// Connection Pooling
		WithMaxIdleConns(100).
		WithMaxIdleConnsPerHost(50).
		WithIdleConnTimeout(90 * time.Second).

		// TLS Fingerprint Profile
		WithTLSProfile(utls.HelloChrome_120).

		// Timeouts
		WithTimeout(30 * time.Second).
		WithDialTimeout(10 * time.Second).

		// Optional Browser Headers
		WithBrowserHeaders().

		// Zero-Dependency Rate Limiting
		WithRateLimit(2.0, 5).                             // 2 req/s with burst of 5
		WithRequestDelay(250*time.Millisecond, 1*time.Second). // Random jitter

		// Optional Proxy
		// WithProxy("http://proxy.example.com:8080").

		Build()

	if err != nil {
		panic(err)
	}

	// Use client like any standard *http.Client
	_ = client
}
```

---

## 🧪 Testing

```bash
go test -v ./...
```

---

## 📄 License

MIT
