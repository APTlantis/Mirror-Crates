// mirror-crates-fast: initial scaffold focused on home-PC performance
// Single-file Go program to:
// 1) Concurrently download millions of small files (e.g., crates) using HTTP/2
// 2) Verify optional SHA256 checksums (if provided)
// 3) Optionally stream files into rolling .tar.zst bundles (5–10GB) while downloading
// 4) Maintain a manifest for resume/verify
//
// Usage (examples):
//   go mod init mirror-crates-fast && go get github.com/klauspost/compress/zstd@v1.20.0
//   go build -o mirror-crates
//
//   # Preferred: Download actual crates by reading a local crates.io-index mirror
//   # Example index path on Windows: D:\Rust-Crates\crates.io-index
//   ./mirror-crates -index-dir D:\Rust-Crates\crates.io-index -out out -concurrency 256
//   # Limit to first 10k crates for testing
//   ./mirror-crates -index-dir D:\Rust-Crates\crates.io-index -limit 10000 -out out
//   # Include yanked versions as well
//   ./mirror-crates -index-dir D:\Rust-Crates\crates.io-index -include-yanked -out out
//   # Use a different base URL if mirroring from elsewhere
//   ./mirror-crates -index-dir D:\Rust-Crates\crates.io-index -crates-base-url https://crates.local/crates -out out
//
//   # Alternatively: Download from a list of URLs (one per line)
//   ./mirror-crates -list urls.txt -out out -concurrency 256
//
//   # Also bundle into ~8GB rolling tar.zst archives while downloading
//   ./mirror-crates -index-dir D:\Rust-Crates\crates.io-index -bundle -bundle-size-gb 8 -bundles-out bundles
//
// Notes:
// - Reads crates.io-index JSON lines to construct URLs like:
//   https://static.crates.io/crates/{name}/{name}-{version}.crate
// - If present, the index checksum (cksum) is used to verify the downloaded file.
// - The bundler consumes completed files and rotates at size threshold.
// - Tune -concurrency to saturate your link without starving the disk.

package downloader

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Record describes one downloaded object for the manifest.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	URL           string `json:"url"`
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	Retries       int    `json:"retries,omitempty"`
	Status        string `json:"status,omitempty"`
}

// ChecksumEntry is the line format for optional checksum file (JSONL).
// Example line: {"url":"https://.../foo.crate","sha256":"ab12..."}

type ChecksumEntry struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// IndexEntry represents a single JSON line from crates.io-index files.
// Only the fields we need are defined here.
// Example: {"name":"serde","vers":"1.0.147","cksum":"...","yanked":false}
// Note: additional fields (deps, features, etc.) are ignored by json.Unmarshal.
// https://github.com/rust-lang/crates.io-index

type IndexEntry struct {
	Name   string `json:"name"`
	Vers   string `json:"vers"`
	Cksum  string `json:"cksum"`
	Yanked bool   `json:"yanked"`
}

// SafeWriter provides serialized writes for logs/manifests.
type SafeWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (sw *SafeWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}

// Bundler streams files into rolling tar.zst archives.
type Bundler struct {
	enabled     bool
	outDir      string
	targetBytes int64

	mu           sync.Mutex
	currentIdx   int
	currentBytes int64
	tw           *tar.Writer
	zw           *zstd.Encoder
	outFile      *os.File
}

func NewBundler(enabled bool, bundlesOut string, targetGB int64) (*Bundler, error) {
	if !enabled {
		return &Bundler{enabled: false}, nil
	}
	if err := os.MkdirAll(bundlesOut, 0o755); err != nil {
		return nil, err
	}
	b := &Bundler{enabled: true, outDir: bundlesOut, targetBytes: targetGB * (1 << 30)}
	if err := b.rotateLocked(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Bundler) rotateLocked() error {
	if !b.enabled {
		return nil
	}
	// Close existing
	if b.tw != nil {
		b.tw.Close()
	}
	if b.zw != nil {
		b.zw.Close()
	}
	if b.outFile != nil {
		b.outFile.Close()
	}

	name := fmt.Sprintf("bundle-%04d.tar.zst", b.currentIdx)
	path := filepath.Join(b.outDir, name)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		f.Close()
		return err
	}
	tw := tar.NewWriter(zw)

	b.outFile = f
	b.zw = zw
	b.tw = tw
	b.currentBytes = 0
	b.currentIdx++
	return nil
}

func (b *Bundler) AddFile(filePath string, headerName string) error {
	if !b.enabled {
		return nil
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	// Rotate if needed (estimate using uncompressed size as proxy)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.currentBytes+fi.Size() > b.targetBytes {
		if err := b.rotateLocked(); err != nil {
			return err
		}
	}
	// Open file and add to tar
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := &tar.Header{
		Name:    headerName,
		Mode:    0o644,
		Size:    fi.Size(),
		ModTime: time.Unix(0, 0), // stable
		Uid:     0,
		Gid:     0,
	}
	if err := b.tw.WriteHeader(hdr); err != nil {
		return err
	}
	n, err := io.Copy(b.tw, f)
	if err != nil {
		return err
	}
	b.currentBytes += n
	return nil
}

func (b *Bundler) Close() error {
	if !b.enabled {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tw != nil {
		if err := b.tw.Close(); err != nil {
			return err
		}
	}
	if b.zw != nil {
		if err := b.zw.Close(); err != nil {
			return err
		}
	}
	if b.outFile != nil {
		return b.outFile.Close()
	}
	return nil
}

// Downloader holds state for concurrent fetching.
type Downloader struct {
	client       *http.Client
	outDir       string
	checksums    map[string]string // url -> sha256 (hex)
	concurrency  int
	timeout      time.Duration
	progressEach int64         // log progress every N files (0=disabled)
	progressIntv time.Duration // periodic progress interval (0=disabled)

	recordsW *SafeWriter
	bundler  *Bundler

	countsMu sync.Mutex
	total    int64
	okCount  int64
	errCount int64

	// retry settings
	retries   int
	retryBase time.Duration
	retryMax  time.Duration

	startedAt time.Time
}

// Metrics
var (
	metOnce     sync.Once
	metRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "crates_download_requests_total", Help: "Download attempts by status and HTTP code"},
		[]string{"status", "code"},
	)
	metBytes     = prometheus.NewCounter(prometheus.CounterOpts{Name: "crates_download_bytes_total", Help: "Total bytes downloaded"})
	metDuration  = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "crates_download_duration_seconds", Help: "Time spent per download attempt", Buckets: prometheus.DefBuckets})
	metRetries   = prometheus.NewCounter(prometheus.CounterOpts{Name: "crates_download_retries_total", Help: "Total retry attempts"})
	metInflight  = prometheus.NewGauge(prometheus.GaugeOpts{Name: "crates_download_inflight", Help: "In-flight HTTP requests"})
	metProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "crates_processed_total", Help: "Processed records by result"},
		[]string{"result"},
	)
)

func initMetrics() {
	metOnce.Do(func() {
		prometheus.MustRegister(metRequests, metBytes, metDuration, metRetries, metInflight, metProcessed)
	})
}

func serveMetrics(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	// Minimal JSON status endpoint for future GUI
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		type status struct {
			Version   string `json:"version"`
			Processed int64  `json:"processed"`
			OK        int64  `json:"ok"`
			Errors    int64  `json:"errors"`
			UptimeSec int64  `json:"uptime_sec"`
			Rate      string `json:"rate_per_sec"`
		}
		// Best-effort snapshot; rate derived from Prom is non-trivial here, so omit if unknown.
		// We expose counts via theDownloaderSnapshot helper.
		processed, ok, errc, startedAt, rate := theDownloaderSnapshot()
		st := status{
			Version:   "dev",
			Processed: processed,
			OK:        ok,
			Errors:    errc,
			UptimeSec: int64(time.Since(startedAt).Seconds()),
			Rate:      rate,
		}
		b, _ := json.Marshal(st)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() {
		slog.Info("metrics/pprof listening", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("metrics server error", "err", err)
		}
	}()
}

// StartMetricsServer exposes Prometheus metrics and pprof handlers when addr is non-empty.
func StartMetricsServer(addr string) {
	if addr == "" {
		return
	}
	initMetrics()
	serveMetrics(addr)
}

// global snapshot hooks for status (set by NewDownloader)
var (
	snapMu   sync.RWMutex
	snapFunc func() (processed, ok, errc int64, started time.Time, rate string)
)

func theDownloaderSnapshot() (processed, ok, errc int64, started time.Time, rate string) {
	snapMu.RLock()
	f := snapFunc
	snapMu.RUnlock()
	if f == nil {
		return 0, 0, 0, time.Now().Add(-time.Second), ""
	}
	return f()
}

// increment helpers avoid 64-bit atomic ops on 32-bit architectures
func (d *Downloader) incOK() {
	d.countsMu.Lock()
	d.okCount++
	d.countsMu.Unlock()
}

func (d *Downloader) incErr() {
	d.countsMu.Lock()
	d.errCount++
	d.countsMu.Unlock()
}

func (d *Downloader) incTotal() int64 {
	d.countsMu.Lock()
	d.total++
	t := d.total
	d.countsMu.Unlock()
	return t
}

func (d *Downloader) snapshotCounts() (ok int64, err int64) {
	d.countsMu.Lock()
	ok = d.okCount
	err = d.errCount
	d.countsMu.Unlock()
	return
}

// DefaultConcurrency returns an aggressive yet safe default for high-throughput mirroring.
func DefaultConcurrency() int {
	return max(64, runtime.NumCPU()*32)
}

func NewDownloader(outDir string, concurrency int, timeout time.Duration, checksums map[string]string, recordsW io.Writer, bundler *Bundler) *Downloader {
	// HTTP client tuned for many concurrent requests
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          concurrency * 4,
		MaxIdleConnsPerHost:   concurrency * 4,
		MaxConnsPerHost:       concurrency * 2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	cli := &http.Client{Transport: tr, Timeout: timeout}

	d := &Downloader{
		client:       cli,
		outDir:       outDir,
		checksums:    checksums,
		concurrency:  concurrency,
		timeout:      timeout,
		progressEach: 0,
		progressIntv: 0,
		recordsW:     &SafeWriter{w: recordsW},
		bundler:      bundler,
		retries:      6,
		retryBase:    500 * time.Millisecond,
		retryMax:     30 * time.Second,
		startedAt:    time.Now(),
	}
	snapMu.Lock()
	snapFunc = func() (int64, int64, int64, time.Time, string) {
		d.countsMu.Lock()
		total := d.total
		okc := d.okCount
		errc := d.errCount
		d.countsMu.Unlock()
		elapsed := time.Since(d.startedAt).Seconds()
		var rate string
		if elapsed > 0 {
			rate = fmt.Sprintf("%.1f", float64(total)/elapsed)
		}
		return total, okc, errc, d.startedAt, rate
	}
	snapMu.Unlock()
	return d
}

func sanitizeName(u string) string {
	// use last path segment; fallback to hex of hash if empty
	seg := u
	if i := strings.LastIndex(u, "/"); i >= 0 {
		seg = u[i+1:]
	}
	seg = strings.TrimSpace(seg)
	if seg == "" || seg == "/" {
		return hex.EncodeToString(sha256.New().Sum(nil))
	}
	// very light sanitize
	seg = strings.ReplaceAll(seg, "..", "_")
	seg = strings.ReplaceAll(seg, "?", "_")
	seg = strings.ReplaceAll(seg, "&", "_")
	return seg
}

// crateNameFromURL extracts the crate name from a crates download URL like
// https://static.crates.io/crates/{name}/{name}-{version}.crate
func crateNameFromURL(u string) string {
	rest := u
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	// drop host
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[j+1:]
	}
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return ""
}

// crateDirFor mirrors the structure used by Download-Crates.py so that files
// are stored in the same layout as the reference downloader.
func crateDirFor(crateName string, outDir string) string {
	if crateName == "" {
		return outDir
	}
	name := crateName
	if len(name) <= 3 {
		return filepath.Join(outDir, name)
	}
	var firstDir string
	if strings.HasPrefix(name, "1") || strings.HasPrefix(name, "2") || strings.HasPrefix(name, "3") {
		firstDir = name[:1]
	} else {
		if len(name) >= 2 && len(name) > 1 && name[1] == '-' {
			firstDir = name[:2]
		} else {
			firstDir = name[:1]
		}
	}
	secondStart := len(firstDir)
	secondEnd := secondStart + 2
	if secondEnd > len(name) {
		secondEnd = len(name)
	}
	secondDir := name[secondStart:secondEnd]
	return filepath.Join(outDir, firstDir, secondDir)
}

func (d *Downloader) fetchOne(ctx context.Context, url string, filesCh chan<- string) Record {
	rec := Record{SchemaVersion: 1, URL: url, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	name := sanitizeName(url)
	crate := crateNameFromURL(url)
	crateDir := crateDirFor(crate, d.outDir)
	if err := os.MkdirAll(crateDir, 0o755); err != nil {
		rec.Error = err.Error()
		rec.Status = "error"
		d.incErr()
		metProcessed.WithLabelValues("error").Inc()
		return rec
	}
	outPath := filepath.Join(crateDir, name)

	// Skip if exists and checksum (if any) matches
	if _, err := os.Stat(outPath); err == nil {
		if ok, _ := d.verifyFile(outPath, url); ok {
			rec.Path = outPath
			rec.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			rec.OK = true
			rec.Status = "ok"
			d.incOK()
			metProcessed.WithLabelValues("skipped").Inc()
			return rec
		}
	}

	// Create file tmp then rename with retries for transient failures
	tmpPath := outPath + ".part"
	var (
		n          int64
		lastErr    error
		attemptCnt int
	)
	attempts := max(1, d.retries)
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCnt = attempt
		// ensure previous partial is removed
		_ = os.Remove(tmpPath)
		f, err := os.Create(tmpPath)
		if err != nil {
			lastErr = err
			break
		}

		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("User-Agent", "Aptlantis-crates-mirror/0.1")
		metInflight.Inc()
		attemptStart := time.Now()
		decInflight := true
		resp, err := d.client.Do(req)
		if err != nil {
			f.Close()
			_ = os.Remove(tmpPath)
			lastErr = err
			metDuration.Observe(time.Since(attemptStart).Seconds())
			metRequests.WithLabelValues("error", "net").Inc()
		} else {
			if resp.StatusCode == http.StatusOK {
				n, err = io.Copy(f, resp.Body)
				resp.Body.Close()
				f.Close()
				if err == nil {
					if err := os.Rename(tmpPath, outPath); err == nil {
						lastErr = nil
						metBytes.Add(float64(n))
						metDuration.Observe(time.Since(attemptStart).Seconds())
						metRequests.WithLabelValues("ok", strconv.Itoa(resp.StatusCode)).Inc()
						metInflight.Dec()
						decInflight = false
						break
					}
					lastErr = err
				} else {
					lastErr = err
				}
			} else {
				// treat 408/425/429 and 5xx as retryable
				retryable := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooEarly || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
				resp.Body.Close()
				f.Close()
				_ = os.Remove(tmpPath)
				metDuration.Observe(time.Since(attemptStart).Seconds())
				metRequests.WithLabelValues("error", strconv.Itoa(resp.StatusCode)).Inc()
				if !retryable {
					metInflight.Dec()
					decInflight = false
					break
				}
			}
		}
		if decInflight {
			metInflight.Dec()
		}

		if lastErr == nil {
			break
		}

		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			break
		}

		// backoff with exponential + jitter
		if attempt < attempts {
			back := d.retryBase << (attempt - 1)
			if back > d.retryMax {
				back = d.retryMax
			}
			jitter := 0.5 + (float64(time.Now().UnixNano()&0x3ff) / 1024.0) // pseudo randomness without math/rand
			sleep := time.Duration(float64(back) * jitter)
			slog.Warn("retrying", "attempt", attempt, "max", attempts, "backoff", sleep.String(), "url", url, "err", lastErr)
			metRetries.Inc()
			time.Sleep(sleep)
		}
	}
	rec.Retries = max(0, attemptCnt-1)
	if lastErr != nil {
		rec.Error = lastErr.Error()
		rec.Status = "error"
		d.incErr()
		metProcessed.WithLabelValues("error").Inc()
		return rec
	}

	// Verify checksum if provided
	ok, sum := d.verifyFile(outPath, url)
	rec.Path = outPath
	rec.Size = n
	rec.SHA256 = sum
	rec.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	rec.OK = ok
	if !ok {
		d.incErr()
		rec.Error = "checksum mismatch"
		rec.Status = "error"
		metProcessed.WithLabelValues("error").Inc()
		// keep the file for debugging; caller may decide to delete
	} else {
		d.incOK()
		rec.Status = "ok"
		metProcessed.WithLabelValues("ok").Inc()
		// Send to bundler
		if d.bundler != nil && d.bundler.enabled {
			// header path inside tar mirrors subdir structure by url host/path
			headerName := headerPathFor(url, name)
			if err := d.bundler.AddFile(outPath, headerName); err != nil {
				// Log but keep going
				slog.Warn("bundle_failed", "url", url, "err", err.Error())
			}
		}
		if filesCh != nil {
			filesCh <- outPath
		}
	}

	return rec
}

func headerPathFor(url string, base string) string {
	// simple: host + first-level path dirs; otherwise fallback to base
	// We avoid importing net/url to keep this lean; heuristic split
	host := ""
	if strings.HasPrefix(url, "http") {
		// http(s)://host/...
		rest := url
		if i := strings.Index(rest, "://"); i >= 0 {
			rest = rest[i+3:]
		}
		if j := strings.Index(rest, "/"); j >= 0 {
			host = rest[:j]
		} else {
			host = rest
		}
	}
	if host == "" {
		return base
	}
	return filepath.Join(host, base)
}

func (d *Downloader) verifyFile(path, url string) (bool, string) {
	want, ok := d.checksums[url]
	// compute regardless to record sum
	f, err := os.Open(path)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, ""
	}
	got := hex.EncodeToString(h.Sum(nil))
	if ok && want != "" {
		return strings.EqualFold(want, got), got
	}
	return true, got
}

// ProgressEach enables logging after every n processed items when n>0.
func (d *Downloader) ProgressEach(n int64) {
	d.progressEach = n
}

// ProgressInterval emits periodic progress logs when dur>0.
func (d *Downloader) ProgressInterval(dur time.Duration) {
	d.progressIntv = dur
}

// SetRetries overrides the total number of retry attempts for transient errors.
func (d *Downloader) SetRetries(n int) {
	d.retries = n
}

// SetRetryBase adjusts the base exponential backoff duration.
func (d *Downloader) SetRetryBase(dur time.Duration) {
	if dur > 0 {
		d.retryBase = dur
	}
}

// SetRetryMax caps the exponential backoff duration per attempt.
func (d *Downloader) SetRetryMax(dur time.Duration) {
	if dur > 0 {
		d.retryMax = dur
	}
}

// HTTPTransport exposes the underlying transport for advanced tuning.
func (d *Downloader) HTTPTransport() http.RoundTripper {
	return d.client.Transport
}

func (d *Downloader) Run(ctx context.Context, urls []string) error {
	if err := os.MkdirAll(d.outDir, 0o755); err != nil {
		return err
	}

	slog.Info("starting", "urls", len(urls), "concurrency", d.concurrency, "out", d.outDir)
	start := time.Now()

	urlsCh := make(chan string)
	resultsCh := make(chan Record)
	var wg sync.WaitGroup

	// workers
	for i := 0; i < d.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range urlsCh {
				ctxTimeout, cancel := context.WithTimeout(ctx, d.timeout)
				rec := d.fetchOne(ctxTimeout, u, nil)
				cancel()
				resultsCh <- rec
			}
		}()
	}

	// result collector
	var doneCollect sync.WaitGroup
	doneCollect.Add(1)
	go func() {
		defer doneCollect.Done()
		enc := json.NewEncoder(d.recordsW)
		var processed int64
		for rec := range resultsCh {
			enc.Encode(rec)
			processed = d.incTotal()
			if d.progressEach > 0 && processed%d.progressEach == 0 {
				ok, errc := d.snapshotCounts()
				slog.Info("progress", "processed", processed, "ok", ok, "err", errc)
			}
		}
	}()

	// optional periodic progress reporter
	var progressDone chan struct{}
	if d.progressIntv > 0 {
		progressDone = make(chan struct{})
		ticker := time.NewTicker(d.progressIntv)
		go func() {
			defer ticker.Stop()
			var last int64 = -1
			for {
				select {
				case <-ticker.C:
					processed := d.getTotal()
					if processed == last {
						continue
					}
					ok, errc := d.snapshotCounts()
					elapsed := time.Since(start)
					var rate float64
					if elapsed > 0 {
						rate = float64(processed) / elapsed.Seconds()
					}
					slog.Info("progress", "processed", processed, "ok", ok, "err", errc, "elapsed", elapsed.String(), "rate_per_sec", fmt.Sprintf("%.1f", rate))
					last = processed
				case <-progressDone:
					return
				}
			}
		}()
	}

	// feed
	go func() {
		for _, u := range urls {
			urlsCh <- u
		}
		close(urlsCh)
	}()

	wg.Wait()
	close(resultsCh)
	doneCollect.Wait()
	if progressDone != nil {
		close(progressDone)
	}

	if d.bundler != nil {
		d.bundler.Close()
	}

	dur := time.Since(start)
	ok, errc := d.snapshotCounts()
	slog.Info("done", "total", d.getTotal(), "ok", ok, "err", errc, "elapsed", dur.String())
	return nil
}

// ReadURLs loads newline-delimited URLs from listPath, skipping blanks and comments.
func ReadURLs(listPath string) ([]string, error) {
	f, err := os.Open(listPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var urls []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls, s.Err()
}

// ReadChecksums loads expected SHA-256 values from a JSONL file of {url, sha256}.
func ReadChecksums(path string) (map[string]string, error) {
	if path == "" {
		return map[string]string{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	out := make(map[string]string)
	for {
		b, err := r.ReadBytes('\n')
		if len(b) > 0 {
			var ce ChecksumEntry
			if json.Unmarshal(bytes.TrimSpace(b), &ce) == nil {
				if ce.URL != "" && ce.SHA256 != "" {
					out[ce.URL] = strings.ToLower(ce.SHA256)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// ReadCratesFromIndex walks a local crates.io-index tree and returns crate URLs plus checksum hints. walks a local crates.io-index directory and produces crate URLs and checksums.
// - baseURL: typically https://static.crates.io/crates
// - includeYanked: if false, skip entries with yanked=true
// - limit: if >0, stop after collecting this many URLs
func ReadCratesFromIndex(indexDir, baseURL string, includeYanked bool, limit int) ([]string, map[string]string, error) {
	var urls []string
	checks := make(map[string]string)
	baseURL = strings.TrimRight(baseURL, "/")
	stopWalk := errors.New("stopWalk")

	err := filepath.Walk(indexDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if limit > 0 && len(urls) >= limit {
			return stopWalk
		}
		name := info.Name()
		if info.IsDir() {
			if name == ".git" || name == ".github" || name == ".gitignore" {
				return filepath.SkipDir
			}
			return nil
		}
		// skip non-regular files
		if !info.Mode().IsRegular() {
			return nil
		}
		// skip config/readme and other non-index files at root
		if name == "config.json" || strings.EqualFold(name, "README.md") || strings.HasSuffix(name, ".keep") {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
		for s.Scan() {
			if limit > 0 && len(urls) >= limit {
				break
			}
			line := strings.TrimSpace(s.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			var ie IndexEntry
			if err := json.Unmarshal([]byte(line), &ie); err != nil {
				continue // ignore malformed lines
			}
			if ie.Name == "" || ie.Vers == "" {
				continue
			}
			if !includeYanked && ie.Yanked {
				continue
			}
			u := fmt.Sprintf("%s/%s/%s-%s.crate", baseURL, ie.Name, ie.Name, ie.Vers)
			urls = append(urls, u)
			if ie.Cksum != "" {
				checks[u] = strings.ToLower(ie.Cksum)
			}
		}
		f.Close()
		return s.Err()
	})
	if err != nil && !errors.Is(err, stopWalk) {
		return nil, nil, err
	}
	return urls, checks, nil
}

// removed bytesTrimSpace helper in favor of bytes.TrimSpace

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// getTotal returns total processed atomically via the mutex to avoid 64-bit atomics on 32-bit arch.
func (d *Downloader) getTotal() int64 {
	d.countsMu.Lock()
	t := d.total
	d.countsMu.Unlock()
	return t
}
