package brisk

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// SingleflightKeyFunc determines the deduplication key for an HTTP request.
// Returning an empty string disables deduplication for that request.
type SingleflightKeyFunc func(req *http.Request) string

// DefaultSingleflightKey generates a deduplication key based on the HTTP method and full URL.
// Non-GET/HEAD methods are skipped by default.
func DefaultSingleflightKey(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	// Only deduplicate safe idempotent reads by default
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return ""
	}
	return fmt.Sprintf("%s:%s", req.Method, req.URL.String())
}

// SingleflightConfig configures request deduplication.
type SingleflightConfig struct {
	// KeyFunc computes the deduplication key for a request.
	// If nil, DefaultSingleflightKey is used.
	KeyFunc SingleflightKeyFunc
}

type sfCall struct {
	wg       sync.WaitGroup
	resp     *http.Response
	bodyData []byte
	err      error
}

// singleflightGroup manages in-flight requests deduplication without external dependencies.
type singleflightGroup struct {
	mu sync.Mutex
	m  map[string]*sfCall
}

func newSingleflightGroup() *singleflightGroup {
	return &singleflightGroup{
		m: make(map[string]*sfCall),
	}
}

func (g *singleflightGroup) Do(key string, fn func() (*http.Response, []byte, error)) (*http.Response, []byte, error, bool) {
	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.resp, c.bodyData, c.err, true
	}

	c := new(sfCall)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.resp, c.bodyData, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.resp, c.bodyData, c.err, false
}

// singleflightRoundTripper deduplicates in-flight identical HTTP requests.
type singleflightRoundTripper struct {
	next    http.RoundTripper
	group   *singleflightGroup
	keyFunc SingleflightKeyFunc
}

// NewSingleflightRoundTripper creates an http.RoundTripper that deduplicates in-flight requests.
func NewSingleflightRoundTripper(next http.RoundTripper, cfg SingleflightConfig) http.RoundTripper {
	kf := cfg.KeyFunc
	if kf == nil {
		kf = DefaultSingleflightKey
	}
	return &singleflightRoundTripper{
		next:    next,
		group:   newSingleflightGroup(),
		keyFunc: kf,
	}
}

func (s *singleflightRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	key := s.keyFunc(req)
	if key == "" {
		return s.next.RoundTrip(req)
	}

	resp, bodyBytes, err, shared := s.group.Do(key, func() (*http.Response, []byte, error) {
		r, e := s.next.RoundTrip(req)
		if e != nil || r == nil || r.Body == nil {
			return r, nil, e
		}

		// Buffer body so all concurrent callers receive a fresh body
		data, readErr := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		return r, data, nil
	})

	if err != nil {
		return nil, err
	}

	if resp == nil {
		return nil, nil
	}

	// Clone response structure so each caller gets their own independent Body Reader
	clonedResp := *resp
	clonedResp.Header = resp.Header.Clone()
	if bodyBytes != nil {
		clonedResp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	} else {
		clonedResp.Body = io.NopCloser(bytes.NewReader(nil))
	}

	// Add custom header indicating singleflight sharing if shared
	if shared {
		if clonedResp.Header == nil {
			clonedResp.Header = make(http.Header)
		}
		clonedResp.Header.Set("X-Singleflight-Shared", "true")
	}

	return &clonedResp, nil
}

// SingleflightBuilder provides a fluent builder to configure singleflight deduplication.
type SingleflightBuilder struct {
	cfg SingleflightConfig
}

// NewSingleflightBuilder creates a SingleflightBuilder with default settings.
func NewSingleflightBuilder() *SingleflightBuilder {
	return &SingleflightBuilder{
		cfg: SingleflightConfig{
			KeyFunc: DefaultSingleflightKey,
		},
	}
}

// KeyFunc sets a custom key function for singleflight deduplication.
func (sb *SingleflightBuilder) KeyFunc(fn SingleflightKeyFunc) *SingleflightBuilder {
	sb.cfg.KeyFunc = fn
	return sb
}

// Config returns the finalized SingleflightConfig.
func (sb *SingleflightBuilder) Config() SingleflightConfig {
	return sb.cfg
}
