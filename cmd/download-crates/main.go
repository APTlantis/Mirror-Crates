package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/APTlantis/Mirror-Rust-Crates/internal/downloader"
)

func main() {
	defaultConcurrency := downloader.DefaultConcurrency()

	var (
		listPath   = flag.String("list", "", "Path to newline-delimited URL list")
		indexDir   = flag.String("index-dir", "", "Path to local crates.io-index directory (e.g., C:\\Rust-Crates\\crates.io-index)")
		baseURL    = flag.String("crates-base-url", "https://static.crates.io/crates", "Base URL for crates content")
		includeY   = flag.Bool("include-yanked", false, "Include yanked versions from the index")
		limit      = flag.Int("limit", 0, "Limit number of crates to process (0 = no limit)")
		outDir     = flag.String("out", "out", "Directory to store downloaded files")
		conc       = flag.Int("concurrency", defaultConcurrency, "Number of concurrent downloads")
		timeoutSec = flag.Int("timeout", 300, "Per-request timeout in seconds")
		checksPath = flag.String("checksums", "", "Optional JSONL of {url, sha256}")
		manifest   = flag.String("manifest", "manifest.jsonl", "Where to write records (JSONL)")
		bundle     = flag.Bool("bundle", false, "Enable rolling tar.zst bundling while downloading")
		bundleGB   = flag.Int64("bundle-size-gb", 8, "Target bundle size in GB")
		bundlesOut = flag.String("bundles-out", "bundles", "Directory for .tar.zst bundles")
		logFormat  = flag.String("log-format", "text", "Logging format: text|json")
		logLevel   = flag.String("log-level", "info", "Logging level: debug|info|warn|error")
		dryRun     = flag.Bool("dry-run", false, "Validate inputs and estimate work; do not download")
		progIntv   = flag.Duration("progress-interval", 0, "Periodic progress logging interval (e.g., 5s; 0=disabled)")
		progEvery  = flag.Int("progress-every", 0, "Log progress every N processed items (0=disabled)")
		retries    = flag.Int("retries", 6, "Total retry attempts for transient errors")
		retryBase  = flag.Duration("retry-base", 500*time.Millisecond, "Base backoff for retries (exponential with jitter)")
		retryMax   = flag.Duration("retry-max", 30*time.Second, "Max backoff per attempt")
		maxConnsPH = flag.Int("max-conns-per-host", 0, "Override http.Transport MaxConnsPerHost (0=auto)")
		maxIdle    = flag.Int("max-idle-conns", 0, "Override http.Transport MaxIdleConns (0=auto)")
		maxIdlePH  = flag.Int("max-idle-per-host", 0, "Override http.Transport MaxIdleConnsPerHost (0=auto)")
		idleTO     = flag.Duration("idle-timeout", 0, "Override http.Transport IdleConnTimeout (0=auto)")
		tlsTO      = flag.Duration("tls-timeout", 0, "Override http.Transport TLSHandshakeTimeout (0=auto)")
		listenAddr = flag.String("listen", "", "Serve Prometheus metrics and pprof at this address (e.g., :9090)")
	)
	flag.Parse()

	// Basic validations and clamps
	if *conc <= 0 {
		*conc = downloader.DefaultConcurrency()
	}
	if *timeoutSec <= 0 {
		*timeoutSec = 300
	}

	lvl := slog.LevelInfo
	switch strings.ToLower(*logLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error", "err":
		lvl = slog.LevelError
	}
	var handler slog.Handler
	if strings.EqualFold(*logFormat, "json") {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	}
	slog.SetDefault(slog.New(handler))

	if *listPath == "" && *indexDir == "" {
		slog.Error("missing required flag: provide -index-dir or -list")
		flag.CommandLine.SetOutput(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage: download-crates -index-dir <path> -out <dir> [options]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	if *indexDir != "" {
		if fi, err := os.Stat(*indexDir); err != nil || !fi.IsDir() {
			slog.Error("index-dir not found or not a directory", "path", *indexDir, "err", err)
			os.Exit(2)
		}
	}

	var (
		urls []string
		sums map[string]string
		err  error
	)

	if *indexDir != "" {
		urls, sums, err = downloader.ReadCratesFromIndex(*indexDir, *baseURL, *includeY, *limit)
		if err != nil {
			slog.Error("read index failed", "err", err)
			os.Exit(1)
		}
		if *checksPath != "" {
			fileSums, err := downloader.ReadChecksums(*checksPath)
			if err != nil {
				slog.Error("read checksums failed", "err", err)
				os.Exit(1)
			}
			for k, v := range fileSums {
				sums[k] = v
			}
		}
	} else {
		urls, err = downloader.ReadURLs(*listPath)
		if err != nil {
			slog.Error("read list failed", "err", err)
			os.Exit(1)
		}
		sums, err = downloader.ReadChecksums(*checksPath)
		if err != nil {
			slog.Error("read checksums failed", "err", err)
			os.Exit(1)
		}
	}

	bndl, err := downloader.NewBundler(*bundle, *bundlesOut, *bundleGB)
	if err != nil {
		slog.Error("bundler init failed", "err", err)
		os.Exit(1)
	}
	defer bndl.Close()

	recFile, err := os.Create(*manifest)
	if err != nil {
		slog.Error("create manifest failed", "err", err)
		os.Exit(1)
	}
	defer recFile.Close()

	dl := downloader.NewDownloader(*outDir, *conc, time.Duration(*timeoutSec)*time.Second, sums, recFile, bndl)
	if *progEvery > 0 {
		dl.ProgressEach(int64(*progEvery))
	}
	if *progIntv > 0 {
		dl.ProgressInterval(*progIntv)
	}
	if *retries >= 0 {
		dl.SetRetries(*retries)
	}
	if *retryBase > 0 {
		dl.SetRetryBase(*retryBase)
	}
	if *retryMax > 0 {
		dl.SetRetryMax(*retryMax)
	}

	if tr, ok := dl.HTTPTransport().(*http.Transport); ok {
		if *maxConnsPH > 0 {
			tr.MaxConnsPerHost = *maxConnsPH
		}
		if *maxIdle > 0 {
			tr.MaxIdleConns = *maxIdle
		}
		if *maxIdlePH > 0 {
			tr.MaxIdleConnsPerHost = *maxIdlePH
		}
		if *idleTO > 0 {
			tr.IdleConnTimeout = *idleTO
		}
		if *tlsTO > 0 {
			tr.TLSHandshakeTimeout = *tlsTO
		}
	}

	if *listenAddr != "" {
		downloader.StartMetricsServer(*listenAddr)
	}

	if *dryRun {
		// Basic validation and estimation
		if *indexDir == "" && *listPath == "" {
			fmt.Println("dry-run: provide -index-dir or -list")
			os.Exit(2)
		}
		if *indexDir != "" {
			if fi, err := os.Stat(*indexDir); err != nil || !fi.IsDir() {
				fmt.Println("dry-run: index-dir not found or not a directory")
				os.Exit(1)
			}
		}
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			fmt.Println("dry-run: create out dir:", err)
			os.Exit(1)
		}
		fmt.Printf("dry-run ok: urls=%d concurrency=%d out=%s\n", len(urls), *conc, *outDir)
		return
	}

	ctx := context.Background()
	if err := dl.Run(ctx, urls); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}
