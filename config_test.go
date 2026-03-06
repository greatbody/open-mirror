package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	content := `
listen: ":9090"
cache_dir: "/tmp/test-cache"
upstreams:
  - prefix: /ubuntu
    origin: http://archive.ubuntu.com/ubuntu
    metadata_ttl: 1800s
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Listen != ":9090" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":9090")
	}
	if cfg.CacheDir != "/tmp/test-cache" {
		t.Errorf("CacheDir = %q, want %q", cfg.CacheDir, "/tmp/test-cache")
	}
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("len(Upstreams) = %d, want 1", len(cfg.Upstreams))
	}
	u := cfg.Upstreams[0]
	if u.Prefix != "/ubuntu" {
		t.Errorf("Prefix = %q, want %q", u.Prefix, "/ubuntu")
	}
	if u.Origin != "http://archive.ubuntu.com/ubuntu" {
		t.Errorf("Origin = %q, want %q", u.Origin, "http://archive.ubuntu.com/ubuntu")
	}
	if u.MetadataTTL != 1800*time.Second {
		t.Errorf("MetadataTTL = %v, want %v", u.MetadataTTL, 1800*time.Second)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	content := `
upstreams:
  - prefix: ubuntu
    origin: http://example.com/ubuntu/
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	// Default listen address.
	if cfg.Listen != ":8080" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":8080")
	}
	// Default cache dir.
	if cfg.CacheDir != "./cache-data" {
		t.Errorf("CacheDir = %q, want %q", cfg.CacheDir, "./cache-data")
	}
	// Prefix should be normalized with leading /.
	if cfg.Upstreams[0].Prefix != "/ubuntu" {
		t.Errorf("Prefix = %q, want %q", cfg.Upstreams[0].Prefix, "/ubuntu")
	}
	// Origin trailing slash should be trimmed.
	if cfg.Upstreams[0].Origin != "http://example.com/ubuntu" {
		t.Errorf("Origin = %q, want %q", cfg.Upstreams[0].Origin, "http://example.com/ubuntu")
	}
	// Default TTL.
	if cfg.Upstreams[0].MetadataTTL != 1*time.Hour {
		t.Errorf("MetadataTTL = %v, want %v", cfg.Upstreams[0].MetadataTTL, 1*time.Hour)
	}
}

func TestLoadConfig_NoUpstreams(t *testing.T) {
	content := `listen: ":8080"`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(tmpFile)
	if err == nil {
		t.Error("expected error for no upstreams, got nil")
	}
}

func TestIsMetadata_WhitelistFallback(t *testing.T) {
	u := &upstream{UpstreamConfig{
		Prefix: "/ubuntu",
		Origin: "http://example.com",
		// No metadata patterns — uses white-list fallback.
	}}

	tests := []struct {
		path     string
		wantMeta bool
	}{
		// Package files — NOT metadata.
		{"/pool/main/n/nginx/nginx_1.24.0-1_amd64.deb", false},
		{"/pool/main/b/base-files/base-files_12_amd64.udeb", false},
		{"/some/path/package.rpm", false},
		{"/v3.18/main/x86_64/nginx-1.24.0-r1.apk", false},
		{"/core/os/x86_64/nginx-1.24.0-1-x86_64.pkg.tar.zst", false},
		{"/core/os/x86_64/old-1.0-1-x86_64.pkg.tar.xz", false},
		{"/pool/main/g/gcc/gcc_12.3.0-1.dsc", false},
		{"/pool/main/g/gcc/gcc_12.3.0.orig.tar.gz", false},
		{"/pool/main/g/gcc/gcc_12.3.0.orig.tar.xz", false},

		// Metadata files — IS metadata.
		{"/dists/jammy/InRelease", true},
		{"/dists/jammy/Release", true},
		{"/dists/jammy/Release.gpg", true},
		{"/dists/jammy/main/binary-amd64/Packages.gz", true},
		{"/repodata/repomd.xml", true},
		{"/some/random/file.txt", true}, // unknown → metadata (safe default)
		{"/dists/jammy/main/cnf/Commands", true},
	}

	for _, tt := range tests {
		got := u.isMetadata(tt.path)
		if got != tt.wantMeta {
			t.Errorf("isMetadata(%q) = %v, want %v", tt.path, got, tt.wantMeta)
		}
	}
}

func TestIsMetadata_ExplicitPatterns(t *testing.T) {
	u := &upstream{UpstreamConfig{
		Prefix: "/ubuntu",
		Origin: "http://example.com",
		MetadataPatterns: []string{
			"*/InRelease",
			"*/Release",
			"*/Packages*",
		},
	}}

	tests := []struct {
		path     string
		wantMeta bool
	}{
		{"/dists/jammy/InRelease", true},
		{"/dists/noble/Release", true},
		{"/dists/jammy/main/binary-amd64/Packages.gz", true},
		{"/pool/main/n/nginx/nginx_1.0_amd64.deb", false},
		{"/some/random/file.txt", false}, // no pattern match → not metadata
	}

	for _, tt := range tests {
		got := u.isMetadata(tt.path)
		if got != tt.wantMeta {
			t.Errorf("isMetadata(%q) = %v, want %v", tt.path, got, tt.wantMeta)
		}
	}
}
