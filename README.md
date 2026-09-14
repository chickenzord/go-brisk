# brisk

A polite and configurable Go HTTP client designed for reliability and well-behaved network requests. Built on standard library primitives, `brisk` pairs browser TLS profiles via [uTLS](https://github.com/refraction-networking/utls) with built-in rate limiting, respectful retries, and request deduplication.

`brisk` returns a standard `*http.Client`, integrating smoothly with any existing Go code or library that accepts `*http.Client` or `http.RoundTripper`.

---

## ✨ Features

- **Standard `*http.Client` Drop-In**: Seamlessly compatible with standard library interfaces and third-party packages.
- **Browser TLS Profiles**: Employs uTLS to send consistent browser ClientHello signatures (default: Chrome 120) with ALPN `h2`.
- **HTTP/2 & HTTP/1.1**: Full connection multiplexing and protocol support over uTLS.
- **Polite Rate Limiting**: Built-in token bucket (global or per-host) and randomized delay jitter to prevent overwhelming target services.
- **Respectful Retries**: Exponential backoff with jitter and automated `Retry-After` header handling.
- **In-Flight Deduplication**: Singleflight request collapsing to avoid redundant concurrent requests.
- **Connection Management**: Configurable per-host connection pooling, idle timeouts, and rotating proxy support.

---

## 📦 Installation

```bash
go get github.com/chickenzord/go-brisk
```

Requires Go 1.21+.

---

## 🚀 Usage

### Basic

```go
package main

import (
	"fmt"
	"io"
	"log"

	"github.com/chickenzord/go-brisk"
)

func main() {
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
	fmt.Printf("%s\n", body)
}
```

### Configured Client

A practical example showing the essential options most applications configure:

```go
package main

import (
	"time"

	"github.com/chickenzord/go-brisk"
)

func main() {
	client, err := brisk.NewBuilder().
		// Timeouts & Connection Pooling
		WithTimeout(30 * time.Second).
		WithDialTimeout(10 * time.Second).
		WithMaxIdleConnsPerHost(20).

		// TLS Fingerprint & Default Browser Headers
		WithTLSProfile(brisk.TLSProfileChrome120). // Or WithRandomTLSProfile()
		WithHeadersFunc(brisk.DesktopChromeHeaders).

		// Rate Limiting & Delays (Polite Scraping)
		WithRateLimit(5.0, 10).                                // 5 req/s, burst 10
		WithRequestDelay(200*time.Millisecond, 1*time.Second). // Random jitter

		// Retries with Exponential Backoff
		WithRetryBuilder(func(r *brisk.RetryBuilder) {
			r.MaxRetries(3).
				WhenStatus(429, 502, 503, 504).
				InitialBackoff(500 * time.Millisecond).
				RespectRetryAfter(true) // Honors origin Retry-After headers
		}).

		// Request Deduplication
		WithSingleflight(). // Collapses duplicate concurrent GET/HEAD requests

		// Optional Proxy or Proxy Pool
		// WithProxy("http://proxy.example.com:8080").
		// WithProxyPool([]string{"http://proxy1:8080", "http://proxy2:8080"}).

		Build()
	if err != nil {
		panic(err)
	}

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
