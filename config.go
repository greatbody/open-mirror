package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration.
type Config struct {
	Listen    string           `yaml:"listen"`
	CacheDir  string           `yaml:"cache_dir"`
	Upstreams []UpstreamConfig `yaml:"upstreams"`
}

// UpstreamConfig defines a single upstream mirror mapping.
type UpstreamConfig struct {
	Prefix           string        `yaml:"prefix"`
	Origin           string        `yaml:"origin"`
	MetadataPatterns []string      `yaml:"metadata_patterns"`
	MetadataTTL      time.Duration `yaml:"metadata_ttl"`
}

// upstream wraps UpstreamConfig with pre-processed state.
type upstream struct {
	UpstreamConfig
}

// isMetadata checks whether the given request path (relative to the prefix)
// matches any of the configured metadata patterns.
// Uses the white-list approach: known package extensions are treated as
// long-lived cache; everything else is metadata.
func (u *upstream) isMetadata(relPath string) bool {
	// If explicit patterns are configured, use them.
	if len(u.MetadataPatterns) > 0 {
		for _, pattern := range u.MetadataPatterns {
			matched, err := filepath.Match(pattern, relPath)
			if err == nil && matched {
				return true
			}
			// Also try matching just against the dir/file components.
			// filepath.Match doesn't support ** so we do a simple
			// suffix match when the pattern starts with *.
			if strings.HasPrefix(pattern, "*") {
				suffix := pattern[1:] // e.g. "*/InRelease" → "/InRelease"
				if strings.HasSuffix(suffix, "*") {
					// Pattern like "*/Packages*" → suffix="/Packages*"
					// Match if the path contains "/Packages" anywhere.
					core := suffix[:len(suffix)-1] // "/Packages"
					if strings.Contains(relPath, core) {
						return true
					}
				} else if strings.HasSuffix(relPath, suffix) {
					return true
				}
			}
		}
		return false
	}

	// Fallback: white-list approach.
	// Known package file extensions → long-lived cache (NOT metadata).
	packageExtensions := []string{
		".deb", ".udeb", ".rpm", ".apk",
		".pkg.tar.zst", ".pkg.tar.xz",
		".dsc", ".tar.gz", ".tar.xz", ".tar.bz2", ".tar.lz",
		".diff.gz",
	}
	lower := strings.ToLower(relPath)
	for _, ext := range packageExtensions {
		if strings.HasSuffix(lower, ext) {
			return false
		}
	}
	// Everything else is metadata.
	return true
}

// LoadConfig reads and parses the YAML config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{
		Listen:   ":8080",
		CacheDir: "./cache-data",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Validate.
	if len(cfg.Upstreams) == 0 {
		return nil, fmt.Errorf("no upstreams configured")
	}
	for i := range cfg.Upstreams {
		u := &cfg.Upstreams[i]
		if u.Prefix == "" || u.Origin == "" {
			return nil, fmt.Errorf("upstream #%d: prefix and origin are required", i)
		}
		// Ensure prefix starts with / and has no trailing /.
		u.Prefix = "/" + strings.Trim(u.Prefix, "/")
		// Ensure origin has no trailing /.
		u.Origin = strings.TrimRight(u.Origin, "/")
		// Default TTL for metadata.
		if u.MetadataTTL == 0 {
			u.MetadataTTL = 1 * time.Hour
		}
	}

	return cfg, nil
}
