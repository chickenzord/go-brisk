package brisk_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chickenzord/go-brisk"
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
		WithHeadersFunc(brisk.MobileChromeHeaders).
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
	defer func() { _ = resp.Body.Close() }()

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
		WithTLSProfile(brisk.TLSProfileChrome120).
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
	defer func() { _ = resp.Body.Close() }()

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
		WithHeadersFunc(brisk.DesktopChromeHeaders).
		WithTimeout(15 * time.Second).
		Build()
	if err != nil {
		t.Fatalf("failed to build client: %v", err)
	}

	resp, err := client.Get("https://example.com")
	if err != nil {
		t.Skipf("network unavailable or error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
}

func TestRetryMiddleware(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	}))
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithRetryConfig(brisk.RetryConfig{
			MaxRetries:        3,
			InitialBackoff:    5 * time.Millisecond,
			MaxBackoff:        50 * time.Millisecond,
			RespectRetryAfter: true,
		}).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("client.Get failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "success" {
		t.Errorf("expected 'success', got %q", string(body))
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestSingleflightMiddleware(t *testing.T) {
	var requestCount int
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("deduped response"))
	}))
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithSingleflight().
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]string, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, reqErr := client.Get(ts.URL)
			if reqErr != nil {
				t.Errorf("request %d failed: %v", idx, reqErr)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			results[idx] = string(b)
		}(i)
	}

	wg.Wait()

	mu.Lock()
	count := requestCount
	mu.Unlock()

	// Only 1 or at most 2 requests should have actually hit the server due to singleflight
	if count > 2 {
		t.Errorf("expected in-flight deduplication to hit server <= 2 times, got %d", count)
	}

	for i, res := range results {
		if res != "deduped response" {
			t.Errorf("result %d expected 'deduped response', got %q", i, res)
		}
	}
}

func TestHostRateLimiter(t *testing.T) {
	hl := brisk.NewHostRateLimiter(10, 1) // 10 req/s, burst 1
	ctx := context.Background()

	reqA, _ := http.NewRequestWithContext(ctx, "GET", "http://host-a.com/page", nil)
	reqB, _ := http.NewRequestWithContext(ctx, "GET", "http://host-b.com/page", nil)

	// Host A and Host B should both have their own initial burst available
	start := time.Now()
	if err := hl.WaitRequest(reqA); err != nil {
		t.Fatalf("reqA 1 failed: %v", err)
	}
	if err := hl.WaitRequest(reqB); err != nil {
		t.Fatalf("reqB 1 failed: %v", err)
	}

	if time.Since(start) > 50*time.Millisecond {
		t.Errorf("independent hosts should not block each other: %v", time.Since(start))
	}

	// Immediate next request to Host A should wait
	startNext := time.Now()
	if err := hl.WaitRequest(reqA); err != nil {
		t.Fatalf("reqA 2 failed: %v", err)
	}
	if time.Since(startNext) < 80*time.Millisecond {
		t.Errorf("expected host A to wait ~100ms, waited: %v", time.Since(startNext))
	}
}

func TestBuilderFunctionVariants(t *testing.T) {
	customRetryCalled := false
	customKeyCalled := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithRetryConditionFunc(func(resp *http.Response, err error) bool {
			customRetryCalled = true
			return brisk.DefaultRetryCondition(resp, err)
		}).
		WithSingleflightKeyFunc(func(req *http.Request) string {
			customKeyCalled = true
			return brisk.DefaultSingleflightKey(req)
		}).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	_ = resp.Body.Close()

	if !customRetryCalled {
		t.Errorf("expected custom retry condition function to be called")
	}
	if !customKeyCalled {
		t.Errorf("expected custom singleflight key function to be called")
	}
}

func TestNestedRetryBuilder(t *testing.T) {
	attempts := 0
	var retryAttemptsObserved []int
	var retryBackoffsObserved []time.Duration

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 4 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("nested builder ok"))
	}))
	defer ts.Close()

	// Use custom Sleeper to make backoff instantaneous in tests
	client, err := brisk.NewBuilder().
		WithRetryBuilder(func(r *brisk.RetryBuilder) {
			r.MaxRetries(5).
				WhenStatus(http.StatusServiceUnavailable).
				Sleeper(func(ctx context.Context, d time.Duration) error {
					return nil // 0ms test sleep
				}).
				OnRetry(func(req *http.Request, resp *http.Response, err error, attempt int, backoff time.Duration) {
					retryAttemptsObserved = append(retryAttemptsObserved, attempt)
					retryBackoffsObserved = append(retryBackoffsObserved, backoff)
				})
		}).
		Build()

	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("client.Get failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "nested builder ok" {
		t.Fatalf("unexpected body: %s", string(body))
	}
	if attempts != 4 {
		t.Errorf("expected 4 attempts, got %d", attempts)
	}
	if len(retryAttemptsObserved) != 3 {
		t.Errorf("expected 3 onRetry calls, got %d", len(retryAttemptsObserved))
	}
}

func TestCustomMiddleware(t *testing.T) {
	middlewareExecuted := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	customMw := func(next http.RoundTripper) http.RoundTripper {
		return &mockRoundTripper{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				middlewareExecuted = true
				return next.RoundTrip(req)
			},
		}
	}

	client, err := brisk.NewBuilder().
		WithMiddleware(customMw).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	_ = resp.Body.Close()

	if !middlewareExecuted {
		t.Errorf("expected custom middleware to be executed")
	}
}

type mockRoundTripper struct {
	roundTrip func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

func TestRotatingProxyPool(t *testing.T) {
	pool, err := brisk.NewRotatingProxyPool(
		"http://proxy1.example.com:8080",
		"http://proxy2.example.com:8080",
		"http://proxy3.example.com:8080",
	)
	if err != nil {
		t.Fatalf("failed to create proxy pool: %v", err)
	}

	proxyFn := pool.ProxyFunc()
	req, _ := http.NewRequest("GET", "https://example.com", nil)

	p1, _ := proxyFn(req)
	p2, _ := proxyFn(req)
	p3, _ := proxyFn(req)
	p4, _ := proxyFn(req)

	if p1.Host != "proxy1.example.com:8080" {
		t.Errorf("expected proxy1, got %s", p1.Host)
	}
	if p2.Host != "proxy2.example.com:8080" {
		t.Errorf("expected proxy2, got %s", p2.Host)
	}
	if p3.Host != "proxy3.example.com:8080" {
		t.Errorf("expected proxy3, got %s", p3.Host)
	}
	if p4.Host != "proxy1.example.com:8080" {
		t.Errorf("expected wrapped to proxy1, got %s", p4.Host)
	}
}

func TestRedirectPolicies(t *testing.T) {
	redirectHit := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirectHit = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("landed"))
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer ts.Close()

	// 1. WithDisableRedirects
	clientNoRedirect, err := brisk.NewBuilder().
		WithDisableRedirects().
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := clientNoRedirect.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected 302 Found without following redirect, got %d", resp.StatusCode)
	}
	if redirectHit {
		t.Errorf("expected redirect NOT to be followed")
	}

	// 2. Default should follow redirect
	clientDefault, _ := brisk.New()
	resp2, err := clientDefault.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	_ = resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK after following redirect, got %d", resp2.StatusCode)
	}
	if !redirectHit {
		t.Errorf("expected redirect to have been followed")
	}
}

func TestCustomDialContext(t *testing.T) {
	dialCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	realDialer := &net.Dialer{Timeout: 5 * time.Second}
	client, err := brisk.NewBuilder().
		WithDialContext(func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialCalled = true
			return realDialer.DialContext(ctx, network, addr)
		}).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	_ = resp.Body.Close()

	if !dialCalled {
		t.Errorf("expected custom dialer to be called")
	}
}

func TestProxyEvictionAndDirectFallback(t *testing.T) {
	// 1. All proxies fail and FallbackDirect is false -> returns error
	poolNoFallback, err := brisk.NewRotatingProxyPoolWithConfig(brisk.ProxyPoolConfig{
		MaxFails:       1,
		Cooldown:       10 * time.Minute,
		FallbackDirect: false,
	}, "http://proxy1.example.com:8080")
	if err != nil {
		t.Fatalf("NewRotatingProxyPoolWithConfig failed: %v", err)
	}

	pURL, err := poolNoFallback.Next()
	if err != nil || pURL == nil {
		t.Fatalf("expected healthy first node, got %v, %v", pURL, err)
	}

	// Report failure to trigger eviction
	poolNoFallback.ReportFailure(pURL)

	_, err = poolNoFallback.Next()
	if err == nil {
		t.Errorf("expected error when all proxies evicted and FallbackDirect=false")
	}

	// 2. All proxies fail but FallbackDirect is true -> returns nil (direct connection fallback)
	poolWithFallback, err := brisk.NewRotatingProxyPoolWithConfig(brisk.ProxyPoolConfig{
		MaxFails:       1,
		Cooldown:       10 * time.Minute,
		FallbackDirect: true,
	}, "http://proxy1.example.com:8080")
	if err != nil {
		t.Fatalf("NewRotatingProxyPoolWithConfig failed: %v", err)
	}

	pURL2, _ := poolWithFallback.Next()
	poolWithFallback.ReportFailure(pURL2)

	fallbackURL, err := poolWithFallback.Next()
	if err != nil {
		t.Errorf("expected nil error on direct fallback, got %v", err)
	}
	if fallbackURL != nil {
		t.Errorf("expected nil proxy URL for direct fallback, got %v", fallbackURL)
	}
}

func TestDynamicTLSProfileRotation(t *testing.T) {
	profilesChosen := make(map[string]bool)
	selector := brisk.NewProfileRotator(
		brisk.TLSProfileChrome120,
		brisk.TLSProfileFirefox120,
	)

	for i := 0; i < 20; i++ {
		p := selector()
		profilesChosen[p.String()] = true
	}

	if len(profilesChosen) < 2 {
		t.Errorf("expected multiple distinct TLS profiles selected, got %v", profilesChosen)
	}
}

type mockResolver struct {
	resolvedIP string
	lookup     func(ctx context.Context, host string) ([]net.IPAddr, error)
}

func (m *mockResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if m.lookup != nil {
		return m.lookup(ctx, host)
	}
	ip := net.ParseIP(m.resolvedIP)
	if ip == nil {
		return nil, fmt.Errorf("invalid ip: %s", m.resolvedIP)
	}
	return []net.IPAddr{{IP: ip}}, nil
}

func TestCustomResolver(t *testing.T) {
	var dialedAddr string
	mockDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialedAddr = addr
		// Return a closed pipe or dummy conn
		c1, c2 := net.Pipe()
		_ = c1.Close()
		return c2, nil
	}

	resolver := &mockResolver{resolvedIP: "10.0.0.1"}
	wrappedDial := brisk.DialContextWithResolver(mockDial, resolver)

	_, _ = wrappedDial(context.Background(), "tcp", "example.com:443")

	if dialedAddr != "10.0.0.1:443" {
		t.Errorf("expected custom resolver to replace host with 10.0.0.1:443, got %s", dialedAddr)
	}
}

func TestHeaderPresets(t *testing.T) {
	// Desktop Chrome Preset
	desktopHeaders := brisk.DesktopChromeHeaders()
	if desktopHeaders["Sec-CH-UA-Platform"] != `"Windows"` {
		t.Errorf("expected Windows platform, got %s", desktopHeaders["Sec-CH-UA-Platform"])
	}
	if desktopHeaders["Sec-CH-UA-Mobile"] != "?0" {
		t.Errorf("expected desktop mobile flag ?0, got %s", desktopHeaders["Sec-CH-UA-Mobile"])
	}

	// Mobile Chrome Preset
	mobileHeaders := brisk.MobileChromeHeaders()
	if mobileHeaders["Sec-CH-UA-Platform"] != `"Android"` {
		t.Errorf("expected Android platform, got %s", mobileHeaders["Sec-CH-UA-Platform"])
	}
	if mobileHeaders["Sec-CH-UA-Mobile"] != "?1" {
		t.Errorf("expected mobile flag ?1, got %s", mobileHeaders["Sec-CH-UA-Mobile"])
	}

	// Custom Preset function passed to WithBrowserHeaders
	customPreset := func() map[string]string {
		return map[string]string{
			"User-Agent": "CustomScraper/2.0",
			"X-Bot":      "Stealth",
		}
	}

	var capturedHeaders http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithHeadersFunc(customPreset).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	_ = resp.Body.Close()

	if capturedHeaders.Get("User-Agent") != "CustomScraper/2.0" {
		t.Errorf("expected CustomScraper/2.0, got %s", capturedHeaders.Get("User-Agent"))
	}
	if capturedHeaders.Get("X-Bot") != "Stealth" {
		t.Errorf("expected Stealth, got %s", capturedHeaders.Get("X-Bot"))
	}
}

func TestMultipleHeaderPresetsLayering(t *testing.T) {
	basePreset := func() map[string]string {
		return map[string]string{
			"User-Agent":      "BaseAgent/1.0",
			"Accept-Language": "en-US",
			"X-Base":          "true",
		}
	}

	overridePreset := func() map[string]string {
		return map[string]string{
			"User-Agent":      "OverriddenAgent/2.0",
			"Accept-Language": "de-DE,de;q=0.9",
			"X-Extra":         "extra-val",
		}
	}

	var capturedHeaders http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Calling WithHeadersFunc multiple times allows chaining/overriding
	client, err := brisk.NewBuilder().
		WithHeadersFunc(basePreset).
		WithHeadersFunc(overridePreset).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	_ = resp.Body.Close()

	if capturedHeaders.Get("User-Agent") != "OverriddenAgent/2.0" {
		t.Errorf("expected User-Agent to be overridden, got %s", capturedHeaders.Get("User-Agent"))
	}
	if capturedHeaders.Get("Accept-Language") != "de-DE,de;q=0.9" {
		t.Errorf("expected Accept-Language to be overridden, got %s", capturedHeaders.Get("Accept-Language"))
	}
	if capturedHeaders.Get("X-Base") != "true" {
		t.Errorf("expected X-Base to be preserved, got %s", capturedHeaders.Get("X-Base"))
	}
	if capturedHeaders.Get("X-Extra") != "extra-val" {
		t.Errorf("expected X-Extra from second preset, got %s", capturedHeaders.Get("X-Extra"))
	}
}

func TestBuilderConvenienceSetters(t *testing.T) {
	// Test MustBuild success
	client := brisk.NewBuilder().
		WithUserAgent("CustomBrisk/1.0").
		WithDisableHTTP2(true).
		WithRateLimit(100, 10).
		WithHostRateLimit(50, 5).
		WithRequestDelay(time.Millisecond, 2*time.Millisecond).
		WithLimiter(brisk.NewTokenBucket(10, 1)).
		WithDNS("1.1.1.1:53").
		WithCookieJar(nil).
		WithMaxRedirects(3).
		MustBuild()

	if client == nil {
		t.Fatal("expected non-nil client from MustBuild")
	}

	// Test MustBuild panic on error
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected MustBuild to panic on invalid proxy, but did not")
		}
	}()
	_ = brisk.NewBuilder().WithProxy("://bad-url").MustBuild()
}

func TestBuilderMaxRedirects(t *testing.T) {
	var redirectCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCount++
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer ts.Close()

	client, err := brisk.NewBuilder().
		WithMaxRedirects(2).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	_, err = client.Get(ts.URL)
	if err == nil {
		t.Fatal("expected error due to max redirects limit, got nil")
	}
	if !strings.Contains(err.Error(), "stopped after 2 redirects") {
		t.Errorf("expected stopped after 2 redirects error, got: %v", err)
	}
}

func TestBuilderProxyOptions(t *testing.T) {
	// Empty proxy should unset proxyFunc
	b := brisk.NewBuilder().WithProxy("")
	if b == nil {
		t.Fatal("expected non-nil builder")
	}

	// Invalid proxy URL should set builder error
	bErr := brisk.NewBuilder().WithProxy("://bad proxy URL")
	_, err := bErr.Build()
	if err == nil {
		t.Errorf("expected error on invalid proxy URL, got nil")
	}

	// Valid proxy URL
	client, err := brisk.NewBuilder().WithProxy("http://127.0.0.1:8080").Build()
	if err != nil {
		t.Fatalf("Build failed with valid proxy URL: %v", err)
	}
	if client == nil {
		t.Fatal("expected client")
	}

	// Custom ProxyFunc
	client2, err := brisk.NewBuilder().WithProxyFunc(func(r *http.Request) (*url.URL, error) {
		return nil, nil
	}).Build()
	if err != nil {
		t.Fatalf("Build with WithProxyFunc failed: %v", err)
	}
	if client2 == nil {
		t.Fatal("expected client2")
	}

	// WithProxyPool and WithProxyPoolBuilder
	client3, err := brisk.NewBuilder().
		WithProxyPool([]string{"http://10.0.0.1:8080", "http://10.0.0.2:8080"}).
		Build()
	if err != nil {
		t.Fatalf("Build with WithProxyPool failed: %v", err)
	}
	if client3 == nil {
		t.Fatal("expected client3")
	}

	client4, err := brisk.NewBuilder().
		WithProxyPoolBuilder([]string{"http://10.0.0.1:8080"}, func(pb *brisk.ProxyPoolBuilder) {
			pb.MaxFails(5).Cooldown(10 * time.Second).FallbackDirect(true)
		}).
		Build()
	if err != nil {
		t.Fatalf("Build with WithProxyPoolBuilder failed: %v", err)
	}
	if client4 == nil {
		t.Fatal("expected client4")
	}

	// Error in WithProxyPoolBuilder (empty urls)
	_, err = brisk.NewBuilder().WithProxyPoolBuilder(nil, nil).Build()
	if err == nil {
		t.Error("expected error with empty proxy pool urls, got nil")
	}
}

func TestProxyPoolReportSuccessAndCooldown(t *testing.T) {
	pb := brisk.NewProxyPoolBuilder("http://127.0.0.1:8001", "http://127.0.0.1:8002")
	pb.MaxFails(2).Cooldown(50 * time.Millisecond).FallbackDirect(false)
	pool, err := pb.Build()
	if err != nil {
		t.Fatalf("failed to build pool: %v", err)
	}

	u1, _ := pool.Next()
	// Report success on nil and valid URL
	pool.ReportSuccess(nil)
	pool.ReportFailure(nil)

	// Fail u1 twice -> unhealthy
	pool.ReportFailure(u1)
	pool.ReportFailure(u1)

	// Recover u1 with ReportSuccess
	pool.ReportSuccess(u1)

	// Fail both nodes
	uNext1, _ := pool.Next()
	uNext2, _ := pool.Next()
	pool.ReportFailure(uNext1)
	pool.ReportFailure(uNext1)
	pool.ReportFailure(uNext2)
	pool.ReportFailure(uNext2)

	// Now all nodes are unhealthy and FallbackDirect is false
	_, err = pool.Next()
	if err == nil {
		t.Error("expected error when all proxies are unhealthy, got nil")
	}

	// Wait for cooldown to expire
	time.Sleep(60 * time.Millisecond)
	recovered, err := pool.Next()
	if err != nil || recovered == nil {
		t.Fatalf("expected proxy to recover after cooldown, got: %v", err)
	}
}

func TestTLSProfilesAndRotator(t *testing.T) {
	randProfile := brisk.RandomBrowserProfile()
	if randProfile.String() == "" {
		t.Error("expected non-empty profile string")
	}

	// Rotator with empty arguments defaults to standardBrowserProfiles
	rotator := brisk.NewProfileRotator()
	p := rotator()
	if p.String() == "" {
		t.Error("expected valid profile from rotator")
	}

	// Rotator with specific profiles
	rotator2 := brisk.NewProfileRotator(brisk.TLSProfileChrome120, brisk.TLSProfileFirefox120)
	p2 := rotator2()
	if p2.String() == "" {
		t.Error("expected valid profile from custom rotator")
	}

	// Builder TLS methods
	client, err := brisk.NewBuilder().
		WithRandomTLSProfile().
		WithTLSProfileFunc(rotator2).
		WithTLSProfiles([]brisk.TLSProfile{brisk.TLSProfileSafari16, brisk.TLSProfileEdge106}).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected client")
	}
}

func TestMultiLimiter(t *testing.T) {
	l1 := brisk.NewTokenBucket(100, 10)
	l2 := brisk.NewDelayJitter(time.Millisecond, 2*time.Millisecond)
	ml := brisk.NewMultiLimiter(l1, nil, l2)

	ctx := context.Background()
	if err := ml.Wait(ctx); err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := ml.WaitRequest(req); err != nil {
		t.Fatalf("WaitRequest failed: %v", err)
	}

	// HostRateLimiter standalone Wait
	hl := brisk.NewHostRateLimiter(10, 2)
	if err := hl.Wait(ctx); err != nil {
		t.Fatalf("hl.Wait failed: %v", err)
	}
}

func TestDNSResolvers(t *testing.T) {
	resolver := brisk.NewCustomDNSResolver("1.1.1.1:53", 0) // test timeout <= 0 branch
	if resolver == nil {
		t.Fatal("expected resolver")
	}

	client, err := brisk.NewBuilder().
		WithResolver(resolver).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected client")
	}
}

func TestRetryHelpers(t *testing.T) {
	// ParseRetryAfter tests
	d, ok := brisk.ParseRetryAfter("")
	if ok || d != 0 {
		t.Errorf("expected empty header to return (0, false)")
	}

	d, ok = brisk.ParseRetryAfter("-5")
	if ok || d != 0 {
		t.Errorf("expected negative delta to return (0, false)")
	}

	d, ok = brisk.ParseRetryAfter("60")
	if !ok || d != 60*time.Second {
		t.Errorf("expected 60s, got %v", d)
	}

	// HTTP-date format (RFC1123)
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	d, ok = brisk.ParseRetryAfter(future)
	if !ok || d <= 0 || d > 35*time.Second {
		t.Errorf("expected ~30s from HTTP-date, got %v, ok=%v", d, ok)
	}

	// Past HTTP-date
	past := time.Now().Add(-30 * time.Second).UTC().Format(http.TimeFormat)
	d, ok = brisk.ParseRetryAfter(past)
	if !ok || d != 0 {
		t.Errorf("expected 0 for past date, got %v, ok=%v", d, ok)
	}

	// Invalid string
	_, ok = brisk.ParseRetryAfter("not-a-date-or-number")
	if ok {
		t.Error("expected false for invalid Retry-After value")
	}

	// Retry conditions: WhenIdempotent & WhenCloudflare
	b := brisk.NewRetryBuilder().
		WhenCloudflare().
		WhenIdempotent().
		InitialBackoff(50 * time.Millisecond).
		MaxBackoff(200 * time.Millisecond).
		BackoffFactor(2.0).
		Jitter(false).
		RespectRetryAfter(true).
		MaxDrainBytes(1024).
		BackoffCalculator(func(attempt int, resp *http.Response) time.Duration {
			return 10 * time.Millisecond
		})

	cfg := b.Config()
	if cfg.CalculateBackoff(1, nil) != 10*time.Millisecond {
		t.Error("expected custom backoff calculation")
	}

	// Test IdempotentRetryCondition
	reqGet, _ := http.NewRequest("GET", "http://example.com", nil)
	reqPost, _ := http.NewRequest("POST", "http://example.com", nil)

	resp500 := &http.Response{StatusCode: 500, Request: reqGet}
	if !brisk.IdempotentRetryCondition(resp500, nil) {
		t.Error("expected GET 500 to be retryable")
	}

	respPost500 := &http.Response{StatusCode: 500, Request: reqPost}
	if brisk.IdempotentRetryCondition(respPost500, nil) {
		t.Error("expected POST 500 NOT to be retryable for IdempotentRetryCondition")
	}

	// Test DefaultRetryCondition error paths (net.Error timeout vs non-timeout)
	timeoutErr := &mockNetError{timeout: true}
	if !brisk.DefaultRetryCondition(nil, timeoutErr) {
		t.Error("expected net timeout error to be retryable")
	}
	nonTimeoutErr := &mockNetError{timeout: false}
	if brisk.DefaultRetryCondition(nil, nonTimeoutErr) {
		t.Error("expected non-timeout net error NOT to be retryable")
	}
	genericErr := errors.New("generic error")
	if !brisk.DefaultRetryCondition(nil, genericErr) {
		t.Error("expected generic non-net error to be retryable")
	}
}

type mockNetError struct {
	timeout bool
}

func (e *mockNetError) Error() string   { return "mock net error" }
func (e *mockNetError) Timeout() bool   { return e.timeout }
func (e *mockNetError) Temporary() bool { return false }

func TestSingleflightEdgeCases(t *testing.T) {
	// Nil request or URL
	if brisk.DefaultSingleflightKey(nil) != "" {
		t.Error("expected empty key for nil request")
	}
	reqNoURL := &http.Request{}
	if brisk.DefaultSingleflightKey(reqNoURL) != "" {
		t.Error("expected empty key for request without URL")
	}

	// POST request (not deduplicated by default)
	reqPost, _ := http.NewRequest("POST", "http://example.com", nil)
	if brisk.DefaultSingleflightKey(reqPost) != "" {
		t.Error("expected empty key for POST request")
	}

	// HEAD request (deduplicated by default)
	reqHead, _ := http.NewRequest("HEAD", "http://example.com/item", nil)
	if brisk.DefaultSingleflightKey(reqHead) != "HEAD:http://example.com/item" {
		t.Errorf("expected HEAD key, got %q", brisk.DefaultSingleflightKey(reqHead))
	}

	// WithSingleflightConfig builder method
	client, err := brisk.NewBuilder().
		WithSingleflightConfig(brisk.SingleflightConfig{KeyFunc: brisk.DefaultSingleflightKey}).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected client")
	}
}

func TestDefaultBrowserHeaders(t *testing.T) {
	headers := brisk.DefaultBrowserHeaders()
	if len(headers) == 0 {
		t.Fatal("expected non-empty default browser headers")
	}
	if headers["Accept"] == "" {
		t.Error("expected Accept header")
	}
}

func TestBuilderRootCAs(t *testing.T) {
	client, err := brisk.NewBuilder().
		WithRootCAs(nil).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected client")
	}
}

func TestRetryBodyRewindFailure(t *testing.T) {
	mockRT := &mockRoundTripper{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(strings.NewReader("server error")),
				Header:     make(http.Header),
			}, nil
		},
	}

	retryRT := brisk.NewRetryRoundTripper(mockRT, brisk.RetryConfig{
		MaxRetries: 2,
		Sleeper: func(ctx context.Context, d time.Duration) error {
			return nil
		},
	})

	// Request with Body but no GetBody -> cannot be rewound, should stop immediately
	req, _ := http.NewRequest("POST", "http://example.com", strings.NewReader("payload"))
	req.GetBody = nil

	resp, err := retryRT.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || resp.StatusCode != 500 {
		t.Errorf("expected 500 response without retry")
	}
}

func TestTransportNilRequest(t *testing.T) {
	tr, err := brisk.NewTransport(brisk.Config{})
	if err != nil {
		t.Fatalf("NewTransport failed: %v", err)
	}

	_, err = tr.RoundTrip(nil)
	if err == nil {
		t.Error("expected error for nil request, got nil")
	}

	reqNoURL := &http.Request{}
	_, err = tr.RoundTrip(reqNoURL)
	if err == nil {
		t.Error("expected error for request with nil URL, got nil")
	}
}

func TestRetryWhenIdempotentBuilder(t *testing.T) {
	b := brisk.NewRetryBuilder().WhenIdempotent()
	cfg := b.Config()

	reqGet, _ := http.NewRequest("GET", "http://example.com", nil)
	respGet := &http.Response{StatusCode: 502, Request: reqGet}
	if !cfg.RetryCondition(respGet, nil) {
		t.Error("expected GET 502 to be retryable")
	}

	reqPost, _ := http.NewRequest("POST", "http://example.com", nil)
	respPost := &http.Response{StatusCode: 502, Request: reqPost}
	if cfg.RetryCondition(respPost, nil) {
		t.Error("expected POST 502 NOT to be retryable")
	}
}

func TestSingleflightErrorAndNilBranches(t *testing.T) {
	// RoundTripper returning error
	errRT := &mockRoundTripper{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("underlying network failure")
		},
	}
	sfRT := brisk.NewSingleflightRoundTripper(errRT, brisk.SingleflightConfig{})
	req, _ := http.NewRequest("GET", "http://example.com/error-test", nil)
	_, err := sfRT.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "underlying network failure") {
		t.Errorf("expected network failure forwarded from singleflight, got: %v", err)
	}

	// RoundTripper returning nil resp & nil error
	nilRT := &mockRoundTripper{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			return nil, nil
		},
	}
	sfRT2 := brisk.NewSingleflightRoundTripper(nilRT, brisk.SingleflightConfig{})
	resp, err := sfRT2.RoundTrip(req)
	if err != nil || resp != nil {
		t.Errorf("expected (nil, nil) from singleflight when next returns (nil, nil)")
	}
}

func TestDialContextWithResolverEdgeCases(t *testing.T) {
	baseDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("base dial called with: " + addr)
	}

	// 1. Nil resolver should return baseDial unchanged
	d1 := brisk.DialContextWithResolver(baseDial, nil)
	_, err := d1(context.Background(), "tcp", "1.1.1.1:80")
	if err == nil || !strings.Contains(err.Error(), "base dial called with: 1.1.1.1:80") {
		t.Errorf("expected base dial called directly")
	}

	// 2. IP address string should bypass resolver lookup
	mockRes := &mockResolver{
		lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return nil, errors.New("resolver should not be called for IP")
		},
	}
	d2 := brisk.DialContextWithResolver(baseDial, mockRes)
	_, err = d2(context.Background(), "tcp", "127.0.0.1:8080")
	if err == nil || !strings.Contains(err.Error(), "base dial called with: 127.0.0.1:8080") {
		t.Errorf("expected IP address to skip DNS resolution: %v", err)
	}

	// 3. DNS lookup returns empty IPs
	emptyRes := &mockResolver{
		lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return []net.IPAddr{}, nil
		},
	}
	d3 := brisk.DialContextWithResolver(baseDial, emptyRes)
	_, err = d3(context.Background(), "tcp", "empty.example.com:80")
	if err == nil || !strings.Contains(err.Error(), "returned no IP addresses") {
		t.Errorf("expected error for empty IP list, got: %v", err)
	}
}

func TestTransportRateLimiterExecution(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// 1. With standard Limiter (cancelled context triggers error in RoundTrip)
	client, err := brisk.NewBuilder().
		WithLimiter(brisk.NewTokenBucket(1, 1)).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET", ts.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	_ = resp.Body.Close()

	// Context cancellation with RequestLimiter (HostRateLimiter)
	clientHostLimit, err := brisk.NewBuilder().
		WithHostRateLimit(1, 1).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	reqCanceled, _ := http.NewRequestWithContext(canceledCtx, "GET", ts.URL, nil)
	_, err = clientHostLimit.Do(reqCanceled)
	if err == nil {
		t.Error("expected error due to canceled context in rate limit, got nil")
	}
}
