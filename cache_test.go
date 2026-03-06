package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempCacheManager(t *testing.T) *CacheManager {
	t.Helper()
	dir := t.TempDir()
	return NewCacheManager(dir)
}

func TestCachePath(t *testing.T) {
	cm := NewCacheManager("/tmp/cache")

	tests := []struct {
		prefix, reqPath, want string
	}{
		{"/ubuntu", "/dists/jammy/InRelease", "/tmp/cache/ubuntu/dists/jammy/InRelease"},
		{"/debian", "/pool/main/n/nginx/nginx_1.0_amd64.deb", "/tmp/cache/debian/pool/main/n/nginx/nginx_1.0_amd64.deb"},
		{"/alpine", "/v3.18/main/x86_64/APKINDEX.tar.gz", "/tmp/cache/alpine/v3.18/main/x86_64/APKINDEX.tar.gz"},
	}

	for _, tt := range tests {
		got := cm.CachePath(tt.prefix, tt.reqPath)
		if got != tt.want {
			t.Errorf("CachePath(%q, %q) = %q, want %q", tt.prefix, tt.reqPath, got, tt.want)
		}
	}
}

func TestLookup_Miss(t *testing.T) {
	cm := tempCacheManager(t)
	cachePath, meta, hit := cm.Lookup("/ubuntu", "/dists/jammy/InRelease", true, 5*time.Minute)
	if hit {
		t.Error("expected cache miss on empty cache")
	}
	if meta != nil {
		t.Error("expected nil metadata on cache miss")
	}
	if cachePath == "" {
		t.Error("expected non-empty cache path even on miss")
	}
}

func TestStoreAndLookup_Package(t *testing.T) {
	cm := tempCacheManager(t)
	body := "fake deb package content"

	// Simulate an upstream HTTP response.
	header := make(http.Header)
	header.Set("Content-Type", "application/octet-stream")
	header.Set("ETag", `"abc123"`)
	resp := &http.Response{
		StatusCode:    200,
		ContentLength: int64(len(body)),
		Header:        header,
	}

	// Store.
	writer, closeFn, err := cm.Store("/ubuntu", "/pool/main/n/nginx/nginx_1.0_amd64.deb", resp, false)
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}
	if _, err := io.Copy(writer, strings.NewReader(body)); err != nil {
		cm.Abort(writer)
		t.Fatalf("writing body: %v", err)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("closeFn() error: %v", err)
	}

	// Lookup — should be a hit for a package (non-metadata), TTL irrelevant.
	cachePath, meta, hit := cm.Lookup("/ubuntu", "/pool/main/n/nginx/nginx_1.0_amd64.deb", false, 0)
	if !hit {
		t.Fatal("expected cache hit after Store")
	}
	if meta.StatusCode != 200 {
		t.Errorf("meta.StatusCode = %d, want 200", meta.StatusCode)
	}
	if meta.Headers["ETag"] != `"abc123"` {
		t.Errorf("meta.Headers[ETag] = %q, want %q", meta.Headers["ETag"], `"abc123"`)
	}
	if meta.ContentLength != int64(len(body)) {
		t.Errorf("meta.ContentLength = %d, want %d", meta.ContentLength, len(body))
	}

	// Verify file contents on disk.
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading cache file: %v", err)
	}
	if string(data) != body {
		t.Errorf("cached body = %q, want %q", string(data), body)
	}
}

func TestStoreAndLookup_MetadataTTL(t *testing.T) {
	cm := tempCacheManager(t)
	body := "InRelease content"

	resp := &http.Response{
		StatusCode:    200,
		ContentLength: int64(len(body)),
		Header:        http.Header{},
	}

	writer, closeFn, err := cm.Store("/ubuntu", "/dists/jammy/InRelease", resp, true)
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}
	io.Copy(writer, strings.NewReader(body))
	if err := closeFn(); err != nil {
		t.Fatalf("closeFn() error: %v", err)
	}

	// Lookup with long TTL — should hit.
	_, _, hit := cm.Lookup("/ubuntu", "/dists/jammy/InRelease", true, 1*time.Hour)
	if !hit {
		t.Error("expected cache hit with long TTL")
	}

	// Lookup with zero TTL — should miss (expired immediately).
	_, meta, hit := cm.Lookup("/ubuntu", "/dists/jammy/InRelease", true, 0)
	if hit {
		t.Error("expected cache miss with zero TTL")
	}
	if meta == nil {
		t.Error("expected non-nil metadata even on TTL expiry (for conditional request support)")
	}
}

func TestLookup_CorruptMetadata(t *testing.T) {
	cm := tempCacheManager(t)
	cachePath := cm.CachePath("/ubuntu", "/bad/file")

	// Create directory and write invalid JSON as metadata sidecar.
	os.MkdirAll(filepath.Dir(cachePath), 0755)
	os.WriteFile(cachePath, []byte("data"), 0644)
	os.WriteFile(cachePath+".meta.json", []byte("{invalid json"), 0644)

	_, meta, hit := cm.Lookup("/ubuntu", "/bad/file", false, 0)
	if hit {
		t.Error("expected miss on corrupt metadata")
	}
	if meta != nil {
		t.Error("expected nil metadata on corrupt sidecar")
	}
}

func TestLookup_SizeMismatch(t *testing.T) {
	cm := tempCacheManager(t)
	cachePath := cm.CachePath("/ubuntu", "/mismatched/file")

	os.MkdirAll(filepath.Dir(cachePath), 0755)
	os.WriteFile(cachePath, []byte("short"), 0644) // 5 bytes

	meta := cacheMetadata{
		StatusCode:    200,
		Headers:       map[string]string{},
		CachedAt:      time.Now(),
		ContentLength: 999, // doesn't match 5 bytes on disk
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(cachePath+".meta.json", metaJSON, 0644)

	_, _, hit := cm.Lookup("/ubuntu", "/mismatched/file", false, 0)
	if hit {
		t.Error("expected miss on content-length mismatch")
	}
}

func TestServeFromCache(t *testing.T) {
	cm := tempCacheManager(t)
	body := "served from cache"

	header := make(http.Header)
	header.Set("Content-Type", "text/plain")
	header.Set("ETag", `"xyz"`)
	resp := &http.Response{
		StatusCode:    200,
		ContentLength: int64(len(body)),
		Header:        header,
	}

	writer, closeFn, err := cm.Store("/ubuntu", "/test/file", resp, false)
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}
	io.Copy(writer, strings.NewReader(body))
	closeFn()

	cachePath, meta, hit := cm.Lookup("/ubuntu", "/test/file", false, 0)
	if !hit {
		t.Fatal("expected cache hit")
	}

	// Serve into an httptest.ResponseRecorder.
	w := httptest.NewRecorder()
	if err := cm.ServeFromCache(w, cachePath, meta); err != nil {
		t.Fatalf("ServeFromCache() error: %v", err)
	}

	result := w.Result()
	if result.StatusCode != 200 {
		t.Errorf("status = %d, want 200", result.StatusCode)
	}
	if result.Header.Get("X-Cache") != "HIT" {
		t.Errorf("X-Cache = %q, want %q", result.Header.Get("X-Cache"), "HIT")
	}
	if result.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q, want %q", result.Header.Get("Content-Type"), "text/plain")
	}
	if result.Header.Get("ETag") != `"xyz"` {
		t.Errorf("ETag = %q, want %q", result.Header.Get("ETag"), `"xyz"`)
	}

	resBody, _ := io.ReadAll(result.Body)
	if string(resBody) != body {
		t.Errorf("body = %q, want %q", string(resBody), body)
	}
}

func TestRefreshMeta(t *testing.T) {
	cm := tempCacheManager(t)
	body := "metadata content"

	resp := &http.Response{
		StatusCode:    200,
		ContentLength: int64(len(body)),
		Header: http.Header{
			"Last-Modified": []string{"Thu, 01 Jan 2025 00:00:00 GMT"},
		},
	}

	writer, closeFn, _ := cm.Store("/ubuntu", "/dists/jammy/InRelease", resp, true)
	io.Copy(writer, strings.NewReader(body))
	closeFn()

	// Look up and confirm it's cached.
	cachePath, meta, _ := cm.Lookup("/ubuntu", "/dists/jammy/InRelease", true, 1*time.Hour)
	originalCachedAt := meta.CachedAt

	// Simulate time passing and a 304 response — refresh the timestamp.
	time.Sleep(10 * time.Millisecond) // ensure clock moves
	meta.CachedAt = time.Now()
	if err := cm.RefreshMeta(cachePath, meta); err != nil {
		t.Fatalf("RefreshMeta() error: %v", err)
	}

	// Re-read metadata and check timestamp was updated.
	_, meta2, hit := cm.Lookup("/ubuntu", "/dists/jammy/InRelease", true, 1*time.Hour)
	if !hit {
		t.Fatal("expected hit after refresh")
	}
	if !meta2.CachedAt.After(originalCachedAt) {
		t.Errorf("CachedAt not refreshed: original=%v, new=%v", originalCachedAt, meta2.CachedAt)
	}
}

func TestAbort(t *testing.T) {
	cm := tempCacheManager(t)

	resp := &http.Response{
		StatusCode:    200,
		ContentLength: 100,
		Header:        http.Header{},
	}

	writer, _, err := cm.Store("/ubuntu", "/dists/jammy/abort-test", resp, false)
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}

	// Write partial data, then abort.
	io.Copy(writer, strings.NewReader("partial"))
	cm.Abort(writer)

	// Verify no cache file or metadata was left behind.
	cachePath := cm.CachePath("/ubuntu", "/dists/jammy/abort-test")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Error("expected cache file to not exist after abort")
	}
	if _, err := os.Stat(cachePath + ".meta.json"); !os.IsNotExist(err) {
		t.Error("expected meta file to not exist after abort")
	}
}

func TestStoreAtomicity(t *testing.T) {
	cm := tempCacheManager(t)
	body := "atomic write test"

	resp := &http.Response{
		StatusCode:    200,
		ContentLength: int64(len(body)),
		Header:        http.Header{},
	}

	// First store: populate cache.
	writer, closeFn, _ := cm.Store("/ubuntu", "/atomic/test", resp, false)
	io.Copy(writer, strings.NewReader(body))
	closeFn()

	// Verify no temp files linger in the directory.
	cachePath := cm.CachePath("/ubuntu", "/atomic/test")
	dir := filepath.Dir(cachePath)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".open-mirror-tmp-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}
