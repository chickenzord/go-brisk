package brisk_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chickenzord/go-brisk"
	utls "github.com/refraction-networking/utls"
)

func TestNewDefaultClient(t *testing.T) {
	client, err := brisk.New()
	if err != nil {
		t.Fatalf("brisk.New() failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil *http.Client")
	}
	if client.Transport == nil {
		t.Fatal("expected non-nil client.Transport")
	}
}

func TestBuilderPoolConfig(t *testing.T) {
	customPool := brisk.PoolConfig{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 10,
		MaxConnsPerHost:     5,
		IdleConnTimeout:     45 * time.Second,
	}

	client, err := brisk.NewBuilder().
		WithPoolConfig(customPool).
		WithTimeout(15 * time.Second).
		WithDialTimeout(5 * time.Second).
		Build()

	if err != nil {
		t.Fatalf("builder failed: %v", err)
	}
	if client.Timeout != 15*time.Second {
		t.Fatalf("expected timeout 15s, got %v", client.Timeout)
	}
}

func TestBuilderGranularPoolConfig(t *testing.T) {
	client, err := brisk.NewBuilder().
		WithMaxIdleConns(50).
		WithMaxIdleConnsPerHost(25).
		WithMaxConnsPerHost(10).
		WithIdleConnTimeout(30 * time.Second).
		Build()

	if err != nil {
		t.Fatalf("builder failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestHeaderInjection(t *testing.T) {
	var receivedCustomHeader string
	var receivedUA string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCustomHeader = r.Header.Get("X-Custom-Test")
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithBrowserHeaders().
		WithHeader("X-Custom-Test", "BriskDefaultVal").
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// 1. First request: use defaults
	req, _ := http.NewRequest("GET", ts.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	_ = resp.Body.Close()

	if receivedCustomHeader != "BriskDefaultVal" {
		t.Errorf("expected header 'BriskDefaultVal', got %q", receivedCustomHeader)
	}
	if receivedUA != brisk.DefaultMobileUserAgent {
		t.Errorf("expected default mobile UA, got %q", receivedUA)
	}

	// 2. Second request: caller explicitly overrides header
	req2, _ := http.NewRequest("GET", ts.URL, nil)
	req2.Header.Set("X-Custom-Test", "CallerOverride")
	req2.Header.Set("User-Agent", "CustomBot/1.0")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	_ = resp2.Body.Close()

	if receivedCustomHeader != "CallerOverride" {
		t.Errorf("expected caller override 'CallerOverride', got %q", receivedCustomHeader)
	}
	if receivedUA != "CustomBot/1.0" {
		t.Errorf("expected caller override 'CustomBot/1.0', got %q", receivedUA)
	}
}

func TestTokenBucketLimiter(t *testing.T) {
	tb := brisk.NewTokenBucket(10, 2) // 10 req/s, burst 2

	ctx := context.Background()

	// First 2 should be immediate (burst)
	start := time.Now()
	if err := tb.Wait(ctx); err != nil {
		t.Fatalf("Wait 1 failed: %v", err)
	}
	if err := tb.Wait(ctx); err != nil {
		t.Fatalf("Wait 2 failed: %v", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Errorf("initial burst took too long: %v", time.Since(start))
	}

	// Third call should wait ~100ms
	startThird := time.Now()
	if err := tb.Wait(ctx); err != nil {
		t.Fatalf("Wait 3 failed: %v", err)
	}
	elapsed := time.Since(startThird)
	if elapsed < 80*time.Millisecond {
		t.Errorf("rate limiter waited too little: %v", elapsed)
	}

	// Context cancellation
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tb.Wait(cancelCtx); err == nil {
		t.Errorf("expected context cancellation error, got nil")
	}
}

func TestDelayJitterLimiter(t *testing.T) {
	dj := brisk.NewDelayJitter(30*time.Millisecond, 60*time.Millisecond)
	ctx := context.Background()

	_ = dj.Wait(ctx)
	start := time.Now()
	_ = dj.Wait(ctx)
	elapsed := time.Since(start)

	if elapsed < 25*time.Millisecond {
		t.Errorf("expected delay at least ~30ms, got %v", elapsed)
	}
}

func TestTransportHTTPRoundTrip(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "hello from brisk test server")
	}))
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithTimeout(5 * time.Second).
		Build()
	if err != nil {
		t.Fatalf("failed to build client: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("client.Get failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	if !strings.Contains(string(body), "hello from brisk test server") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestTransportCloseIdleConnections(t *testing.T) {
	transport, err := brisk.NewBuilder().
		WithTLSProfile(utls.HelloChrome_120).
		BuildTransport()
	if err != nil {
		t.Fatalf("failed to build transport: %v", err)
	}

	// Ensure calling CloseIdleConnections does not panic
	transport.CloseIdleConnections()
}

func TestTransportHTTPSRoundTrip(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "secure hello from brisk test server")
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithInsecureSkipVerify(true).
		WithTimeout(5 * time.Second).
		Build()
	if err != nil {
		t.Fatalf("failed to build client: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("client.Get failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	if !strings.Contains(string(body), "secure hello from brisk test server") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestLiveHTTPS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client, err := brisk.NewBuilder().
		WithBrowserHeaders().
		WithTimeout(15 * time.Second).
		Build()
	if err != nil {
		t.Fatalf("failed to build client: %v", err)
	}

	resp, err := client.Get("https://example.com")
	if err != nil {
		t.Skipf("network unavailable or error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
}

