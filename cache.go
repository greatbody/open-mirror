package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cacheMetadata stores HTTP response metadata alongside the cached file.
type cacheMetadata struct {
	StatusCode    int               `json:"status_code"`
	Headers       map[string]string `json:"headers"`
	CachedAt      time.Time         `json:"cached_at"`
	IsMetadata    bool              `json:"is_metadata"`
	ContentLength int64             `json:"content_length"`
}

// CacheManager handles reading and writing cache files.
type CacheManager struct {
	baseDir string
}

// NewCacheManager creates a new CacheManager rooted at baseDir.
func NewCacheManager(baseDir string) *CacheManager {
	return &CacheManager{baseDir: baseDir}
}

// CachePath returns the local file system path for a given upstream prefix + request path.
func (cm *CacheManager) CachePath(prefix, reqPath string) string {
	// reqPath is relative to the prefix, e.g. "/dists/jammy/InRelease"
	// We store under: baseDir / prefix / reqPath
	cleaned := filepath.Clean(strings.TrimPrefix(reqPath, "/"))
	prefixCleaned := filepath.Clean(strings.TrimPrefix(prefix, "/"))
	return filepath.Join(cm.baseDir, prefixCleaned, cleaned)
}

// metaPath returns the path to the metadata sidecar file.
func (cm *CacheManager) metaPath(cachePath string) string {
	return cachePath + ".meta.json"
}

// Lookup checks if a valid cache entry exists.
// Returns the cache file path, metadata, and whether the cache is valid.
func (cm *CacheManager) Lookup(prefix, reqPath string, isMeta bool, ttl time.Duration) (string, *cacheMetadata, bool) {
	cachePath := cm.CachePath(prefix, reqPath)
	metaPath := cm.metaPath(cachePath)

	// Read metadata sidecar.
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return cachePath, nil, false
	}

	var meta cacheMetadata
	if err := json.Unmarshal(metaData, &meta); err != nil {
		slog.Warn("corrupt cache metadata, treating as miss", "path", metaPath, "error", err)
		return cachePath, nil, false
	}

	// Check if the data file exists.
	info, err := os.Stat(cachePath)
	if err != nil {
		return cachePath, nil, false
	}

	// For metadata files, check TTL.
	if isMeta {
		age := time.Since(meta.CachedAt)
		if age > ttl {
			slog.Debug("metadata cache expired",
				"path", reqPath,
				"age", age.Round(time.Second),
				"ttl", ttl,
			)
			return cachePath, &meta, false
		}
	}

	// Sanity check: content length should match if we recorded it.
	if meta.ContentLength > 0 && info.Size() != meta.ContentLength {
		slog.Warn("cache file size mismatch, treating as miss",
			"path", cachePath,
			"expected", meta.ContentLength,
			"actual", info.Size(),
		)
		return cachePath, nil, false
	}

	return cachePath, &meta, true
}

// Store writes the upstream response to the cache.
// It returns an io.Writer that the caller should write the body into.
// The caller must call the returned close function when done.
func (cm *CacheManager) Store(prefix, reqPath string, resp *http.Response, isMeta bool) (io.Writer, func() error, error) {
	cachePath := cm.CachePath(prefix, reqPath)

	// Ensure directory exists.
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, nil, fmt.Errorf("creating cache directory: %w", err)
	}

	// Write to a temp file first, then rename for atomicity.
	tmpFile, err := os.CreateTemp(dir, ".open-mirror-tmp-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating temp file: %w", err)
	}

	// Prepare metadata.
	meta := cacheMetadata{
		StatusCode:    resp.StatusCode,
		Headers:       make(map[string]string),
		CachedAt:      time.Now(),
		IsMetadata:    isMeta,
		ContentLength: resp.ContentLength,
	}

	// Preserve important headers.
	headersToKeep := []string{
		"Content-Type",
		"Content-Length",
		"Last-Modified",
		"ETag",
	}
	for _, h := range headersToKeep {
		if v := resp.Header.Get(h); v != "" {
			meta.Headers[h] = v
		}
	}

	closeFn := func() error {
		// Close the temp file.
		if err := tmpFile.Close(); err != nil {
			os.Remove(tmpFile.Name())
			return fmt.Errorf("closing temp file: %w", err)
		}

		// Rename temp → final.
		if err := os.Rename(tmpFile.Name(), cachePath); err != nil {
			os.Remove(tmpFile.Name())
			return fmt.Errorf("renaming cache file: %w", err)
		}

		// Write metadata sidecar.
		metaJSON, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling metadata: %w", err)
		}
		metaPath := cm.metaPath(cachePath)
		if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
			return fmt.Errorf("writing metadata: %w", err)
		}

		slog.Debug("cached file", "path", reqPath, "size", meta.ContentLength)
		return nil
	}

	return tmpFile, closeFn, nil
}

// Abort removes a temp file if caching failed midway.
// This is a best-effort cleanup.
func (cm *CacheManager) Abort(tmpWriter io.Writer) {
	if f, ok := tmpWriter.(*os.File); ok {
		f.Close()
		os.Remove(f.Name())
	}
}

// RefreshMeta updates the CachedAt timestamp of an existing metadata sidecar
// without touching the data file. Used when upstream returns 304 Not Modified.
func (cm *CacheManager) RefreshMeta(cachePath string, meta *cacheMetadata) error {
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}
	metaPath := cm.metaPath(cachePath)
	if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	return nil
}

// ServeFromCache writes the cached file to the HTTP response.
func (cm *CacheManager) ServeFromCache(w http.ResponseWriter, cachePath string, meta *cacheMetadata) error {
	f, err := os.Open(cachePath)
	if err != nil {
		return fmt.Errorf("opening cache file: %w", err)
	}
	defer f.Close()

	// Restore headers.
	for k, v := range meta.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("X-Cache", "HIT")

	w.WriteHeader(meta.StatusCode)

	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("writing cached response: %w", err)
	}

	return nil
}
