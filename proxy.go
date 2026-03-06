package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

// ProxyHandler handles incoming requests, serving from cache or proxying to upstream.
type ProxyHandler struct {
	config *Config
	cache  *CacheManager
	client *http.Client
	flight singleflight.Group
}

// NewProxyHandler creates a new ProxyHandler.
func NewProxyHandler(cfg *Config) *ProxyHandler {
	return &ProxyHandler{
		config: cfg,
		cache:  NewCacheManager(cfg.CacheDir),
		client: &http.Client{
			Timeout: 5 * time.Minute, // generous timeout for large .deb files
			// Don't follow redirects automatically; let the client handle them
			// or we cache the final destination.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

// ServeHTTP dispatches to the matching upstream.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only support GET and HEAD.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Find matching upstream.
	var matched *UpstreamConfig
	var relPath string
	for i := range p.config.Upstreams {
		u := &p.config.Upstreams[i]
		if strings.HasPrefix(r.URL.Path, u.Prefix+"/") || r.URL.Path == u.Prefix {
			matched = u
			relPath = strings.TrimPrefix(r.URL.Path, u.Prefix)
			if relPath == "" {
				relPath = "/"
			}
			break
		}
	}

	if matched == nil {
		http.Error(w, "No upstream configured for this path", http.StatusNotFound)
		return
	}

	p.handleRequest(w, r, matched, relPath)
}

func (p *ProxyHandler) handleRequest(w http.ResponseWriter, r *http.Request, u *UpstreamConfig, relPath string) {
	up := &upstream{*u}
	isMeta := up.isMetadata(relPath)

	logAttrs := []any{
		"path", r.URL.Path,
		"upstream", u.Prefix,
		"is_metadata", isMeta,
	}

	// 1. Check cache.
	cachePath, meta, valid := p.cache.Lookup(u.Prefix, relPath, isMeta, u.MetadataTTL)
	if valid {
		slog.Info("cache HIT", logAttrs...)
		if err := p.cache.ServeFromCache(w, cachePath, meta); err != nil {
			slog.Error("serving from cache", "error", err, "path", r.URL.Path)
		}
		return
	}

	slog.Info("cache MISS", logAttrs...)

	// 2. Use singleflight to coalesce concurrent requests for the same resource.
	// The singleflight key is the full request path.
	sfKey := u.Prefix + relPath

	// For singleflight, we fetch upstream and cache. But since we need to
	// stream the response, we can't easily return the body through singleflight.
	// Instead, singleflight ensures only one goroutine fetches and caches.
	// Other waiters will serve from the newly written cache once done.

	// Try to be the leader for this fetch.
	resultCh := p.flight.DoChan(sfKey, func() (interface{}, error) {
		result := p.fetchAndCache(r, u, relPath, isMeta, meta)
		return result, result.err
	})

	res := <-resultCh
	if res.Err != nil {
		slog.Error("upstream fetch failed", "error", res.Err, "path", r.URL.Path)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	result := res.Val.(*fetchResult)

	// Serve the freshly cached file.
	if err := p.cache.ServeFromCache(w, result.cachePath, result.meta); err != nil {
		slog.Error("serving freshly cached response", "error", err, "path", r.URL.Path)
	}
}

type fetchResult struct {
	cachePath string
	meta      *cacheMetadata
	err       error
}

func (p *ProxyHandler) fetchAndCache(r *http.Request, u *UpstreamConfig, relPath string, isMeta bool, oldMeta *cacheMetadata) *fetchResult {
	upstreamURL := u.Origin + relPath

	// Create upstream request.
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		return &fetchResult{err: fmt.Errorf("creating upstream request: %w", err)}
	}

	// Forward some headers.
	if ua := r.Header.Get("User-Agent"); ua != "" {
		upReq.Header.Set("User-Agent", ua)
	}

	// If we have old metadata, try conditional request.
	if oldMeta != nil {
		if etag, ok := oldMeta.Headers["ETag"]; ok {
			upReq.Header.Set("If-None-Match", etag)
		}
		if lm, ok := oldMeta.Headers["Last-Modified"]; ok {
			upReq.Header.Set("If-Modified-Since", lm)
		}
	}

	slog.Debug("fetching from upstream", "url", upstreamURL)

	resp, err := p.client.Do(upReq)
	if err != nil {
		return &fetchResult{err: fmt.Errorf("upstream request failed: %w", err)}
	}
	defer resp.Body.Close()

	cachePath := p.cache.CachePath(u.Prefix, relPath)

	// Handle 304 Not Modified.
	if resp.StatusCode == http.StatusNotModified && oldMeta != nil {
		slog.Debug("upstream returned 304, refreshing TTL", "path", relPath)
		// Refresh the cached metadata timestamp.
		oldMeta.CachedAt = time.Now()
		// Re-store just the metadata.
		if err := p.cache.RefreshMeta(cachePath, oldMeta); err != nil {
			slog.Warn("failed to refresh cache metadata", "error", err)
		}
		return &fetchResult{cachePath: cachePath, meta: oldMeta}
	}

	// Only cache 200 OK responses.
	if resp.StatusCode != http.StatusOK {
		return &fetchResult{err: fmt.Errorf("upstream returned %d", resp.StatusCode)}
	}

	// Store the response body in cache.
	writer, closeFn, err := p.cache.Store(u.Prefix, relPath, resp, isMeta)
	if err != nil {
		return &fetchResult{err: fmt.Errorf("preparing cache: %w", err)}
	}

	_, copyErr := io.Copy(writer, resp.Body)
	if copyErr != nil {
		p.cache.Abort(writer)
		return &fetchResult{err: fmt.Errorf("caching response body: %w", copyErr)}
	}

	if err := closeFn(); err != nil {
		return &fetchResult{err: fmt.Errorf("finalizing cache: %w", err)}
	}

	// Read back the metadata we just wrote.
	_, newMeta, valid := p.cache.Lookup(u.Prefix, relPath, isMeta, u.MetadataTTL)
	if !valid || newMeta == nil {
		return &fetchResult{err: fmt.Errorf("cache verification failed after write")}
	}

	return &fetchResult{cachePath: cachePath, meta: newMeta}
}
