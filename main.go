package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Version is the single source of truth for the project version.
// Bump this when preparing a new release.
const Version = "0.1.0"

// Set via -ldflags at build time. Falls back to Version constant.
var (
	version   = Version
	buildTime = "unknown"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	verbose := flag.Bool("v", false, "enable verbose (debug) logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("open-mirror %s (built %s)\n", version, buildTime)
		os.Exit(0)
	}

	// Setup logging.
	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	// Load config.
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("open-mirror starting",
		"version", version,
		"listen", cfg.Listen,
		"cache_dir", cfg.CacheDir,
		"upstreams", len(cfg.Upstreams),
	)

	for _, u := range cfg.Upstreams {
		slog.Info("upstream configured",
			"prefix", u.Prefix,
			"origin", u.Origin,
			"metadata_ttl", u.MetadataTTL,
			"metadata_patterns", strings.Join(u.MetadataPatterns, ", "),
		)
	}

	// Ensure cache directory exists.
	if err := os.MkdirAll(cfg.CacheDir, 0755); err != nil {
		slog.Error("failed to create cache directory", "error", err, "path", cfg.CacheDir)
		os.Exit(1)
	}

	// Create handler.
	handler := NewProxyHandler(cfg)

	// Setup HTTP server.
	mux := http.NewServeMux()

	// Health check endpoint.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// All other requests go to the proxy handler.
	mux.Handle("/", handler)

	server := &http.Server{
		Addr:         cfg.Listen,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // large files may take a while
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("starting server", "addr", cfg.Listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}

	slog.Info("server stopped")
}
