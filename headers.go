package brisk

import (
	"net/http"
)

// HeaderPreset is a function returning a map of HTTP headers.
type HeaderPreset func() map[string]string

const (
	// DefaultMobileUserAgent is a standard Android Chrome Mobile User-Agent.
	DefaultMobileUserAgent = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36"

	// DefaultDesktopUserAgent is a standard Desktop Chrome User-Agent.
	DefaultDesktopUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"
)

// MobileChromeHeaders returns realistic browser headers for Android Chrome.
func MobileChromeHeaders() map[string]string {
	return map[string]string{
		"User-Agent":                DefaultMobileUserAgent,
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.9",
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

// DesktopChromeHeaders returns realistic browser headers for Windows Desktop Chrome.
func DesktopChromeHeaders() map[string]string {
	return map[string]string{
		"User-Agent":                DefaultDesktopUserAgent,
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.9",
		"Sec-CH-UA":                 `"Google Chrome";v="125", "Chromium";v="125", "Not.A/Brand";v="24"`,
		"Sec-CH-UA-Mobile":          "?0",
		"Sec-CH-UA-Platform":        `"Windows"`,
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	}
}

// DefaultBrowserHeaders is the standard default browser header preset (Mobile Chrome).
func DefaultBrowserHeaders() map[string]string {
	return MobileChromeHeaders()
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
	clonedReq := req.Clone(req.Context())

	for k, v := range h.headers {
		if clonedReq.Header.Get(k) == "" {
			clonedReq.Header.Set(k, v)
		}
	}

	return h.next.RoundTrip(clonedReq)
}
