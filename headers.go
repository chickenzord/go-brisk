package brisk

import (
	"net/http"
)

const (
	// DefaultMobileUserAgent is a standard Android Chrome Mobile User-Agent.
	DefaultMobileUserAgent = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36"

	// DefaultDesktopUserAgent is a standard Desktop Chrome User-Agent.
	DefaultDesktopUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"
)

// DefaultBrowserHeaders returns standard browser request headers commonly expected
// by CDN edge caches and bot defense filters.
func DefaultBrowserHeaders() map[string]string {
	return map[string]string{
		"User-Agent":                DefaultMobileUserAgent,
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.9,id;q=0.8",
		"Sec-CH-UA":                 `"Google Chrome";v="125", "Chromium";v="125", "Not.A/Brand";v="24"`,
		"Sec-CH-UA-Mobile":          "?1",
		"Sec-CH-UA-Platform":        `"Android"`,
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	}
}

// headerRoundTripper wraps an existing http.RoundTripper and injects default headers
// into outgoing requests if not already set.
type headerRoundTripper struct {
	next    http.RoundTripper
	headers map[string]string
}

func newHeaderRoundTripper(next http.RoundTripper, headers map[string]string) *headerRoundTripper {
	copied := make(map[string]string, len(headers))
	for k, v := range headers {
		copied[k] = v
	}
	return &headerRoundTripper{
		next:    next,
		headers: copied,
	}
}

// RoundTrip injects the headers into req and forwards to next RoundTripper.
func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone request so original caller's request is not mutated unexpectedly
	clonedReq := req.Clone(req.Context())

	for k, v := range h.headers {
		// Only set if the caller has not already explicitly set this header
		if clonedReq.Header.Get(k) == "" {
			clonedReq.Header.Set(k, v)
		}
	}

	return h.next.RoundTrip(clonedReq)
}
