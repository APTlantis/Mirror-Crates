package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/APTlantis/Mirror-Rust-Crates/internal/sidecar"
)

func main() {
	defaultConcurrency := sidecar.DefaultConcurrency()

	var (
		indexDir         = flag.String("index-dir", "", "Path to local crates.io-index directory (e.g., C:\\Rust-Crates\\crates.io-index)")
		outDir           = flag.String("out", "out", "Directory to write sidecar metadata files")
		includeY         = flag.Bool("include-yanked", false, "Include yanked versions from the index")
		limitFlag        = flag.Int64("limit", 0, "Limit number of entries to write (0 = all)")
		conc             = flag.Int("concurrency", defaultConcurrency, "Number of concurrent index-file workers")
		baseURL          = flag.String("crates-base-url", "https://static.crates.io/crates", "Base URL for crates content")
		logFormat        = flag.String("log-format", "text", "Logging format: text|json")
		logLevel         = flag.String("log-level", "info", "Logging level: debug|info|warn|error")
		progressInterval = flag.Duration("progress-interval", 0, "Periodic progress logging interval (e.g., 5s; 0=disabled)")
		progressEvery    = flag.Int("progress-every", 0, "Log progress every N processed items (0=disabled)")
	)
	flag.Parse()

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

	if *indexDir == "" {
		slog.Error("missing required flag -index-dir")
		flag.CommandLine.SetOutput(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage: generate-sidecars -index-dir <path> -out <dir> [options]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	cfg := sidecar.Config{
		IndexDir:         *indexDir,
		OutDir:           *outDir,
		IncludeYanked:    *includeY,
		Limit:            *limitFlag,
		Concurrency:      *conc,
		BaseURL:          *baseURL,
		ProgressInterval: *progressInterval,
		ProgressEvery:    *progressEvery,
	}

	ctx := context.Background()
	if _, err := sidecar.Generate(ctx, cfg); err != nil {
		slog.Error("sidecar generation failed", "err", err)
		os.Exit(1)
	}
}
