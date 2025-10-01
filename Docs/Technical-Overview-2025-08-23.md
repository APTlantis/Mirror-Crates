# Mirror-Rust-Crates — Technical Overview (2025-08-23)

This document provides a technical snapshot of the Mirror-Rust-Crates repository as of 2025-08-23 15:42 (local). It summarizes architecture, components, data flows, build/run guidance, current status, and planned work.

---

## Executive Summary

Mirror-Rust-Crates is a high-performance toolchain for mirroring crates.io content locally. The core downloader is implemented in Go and optimized for massive concurrency and HTTP/2 connection reuse, with checksum-aware resume, optional on-the-fly bundling to .tar.zst, and JSONL manifest logging for auditability. A Python wrapper helps clone/update the crates.io index and invoke the downloader non-interactively. Additional utilities include a sidecar metadata generator and a directory Archive-Hasher.

At this stage:
- The downloader and sidecar generator are functional and documented in README.md.
- The Python wrapper has been modernized for non-interactive use and corrects earlier invocation mismatch.
- Planning docs (Action-Plan.md, TODO.md, Progress-Log.md) outline logging unification, HTTP robustness, tests, CI, and observability work.
- Archive-Hasher works but uses the deprecated openpgp package and is flagged for modernization.

---

## Repository Layout

- Download-Crates.go — Go command to mirror crates from crates.io (core tool)
- Generate-Sidecars.go — Go command to generate per-crate-version JSON sidecars from crates.io-index
- Clone-Index.py — Python wrapper to clone/update the crates.io index and run the downloader
- Archive-Hasher/ — Independent Go tool for directory hashing/signing and ZIP packaging
  - Archive-Hasher.go — implementation
  - README.md — usage and features
  - go.mod / go.sum — separate module definition
- README.md — primary usage guide (downloader + sidecars + wrapper)
- Action-Plan.md — architecture notes, gaps, and prioritized roadmap
- TODO.md — task breakdown derived from Action-Plan.md
- Progress-Log.md — dated notes tracking progress vs TODO
- LICENSE — MIT
- go.mod / go.sum — root module definition
- manifest.jsonl — example/placeholder of downloader manifest output

---

## Architecture and Components

### 1) Downloader (Download-Crates.go)

Purpose: High-throughput mirroring of crates from crates.io to a local filesystem with optional bundling to rolling .tar.zst archives and manifest logging.

Key concepts:
- Concurrency: default is max(64, 32×CPU cores); configurable via -concurrency.
- HTTP/2: achieved through Go's http.Transport pooling (code imports net/http; the README emphasizes HTTP/2 multiplexing).
- Checksum-aware resume: verifies an existing file against a known SHA-256 before re-downloading when checksum metadata is available (from index or external JSONL).
- Manifest logging: emits one JSONL Record per processed URL (see schema below).
- Bundling: when enabled, streams completed files into size-rotated tar.zst bundles to reduce inode churn.

Flags (from main()):
- -index-dir string: path to local crates.io-index directory (preferred mode)
- -crates-base-url string: base URL to construct crate URLs (default https://static.crates.io/crates)
- -include-yanked: include yanked versions when scanning index
- -limit int: limit number of crates to process (0 = all)
- -list string: newline-delimited list of URLs (alternative to -index-dir)
- -out string: output directory for downloaded files (default out)
- -concurrency int: number of concurrent downloads (default ~32×CPU cores, min 64)
- -timeout int: per-request timeout (seconds; default 300)
- -checksums string: optional JSONL with entries {url, sha256}
- -manifest string: where to write JSONL records (default manifest.jsonl)
- -bundle: enable rolling tar.zst bundling while downloading
- -bundle-size-gb int: target bundle size (GB; default 8)
- -bundles-out string: directory for .tar.zst bundles (default bundles)

Manifest schema (type Record):
- url (string)
- path (string): local file path written by downloader
- size (int64): bytes
- sha256 (string): hex digest of the written file
- started_at (string): RFC3339 timestamp
- finished_at (string): RFC3339 timestamp
- ok (bool): success indicator
- error (string, optional): error details when ok=false

Index scanning: readCratesFromIndex walks crates.io-index files (ignoring .git, .github, .gitignore, config.json, README.md, *.keep), parses each JSON line into IndexEntry{name, vers, cksum, yanked}, filters yanked unless -include-yanked, and builds URLs "{base}/{name}/{name}-{vers}.crate". It also captures cksum (SHA-256) for checksum-aware verification.

Output layout (filesystem): files are sharded under out/ using the crate name. For names longer than 3 characters, first-level shard is either the first character (or a special two-character prefix if the second char is '-') and the second-level shard is the next two characters. For short names (≤3), the name itself is used as the shard directory. Final file: out/<shard>/<shard2>/<name>-<vers>.crate.

Bundling: Bundler writes completed files into rolling tar.zst archives named bundle-####.tar.zst in -bundles-out, rotating when the uncompressed file sizes would exceed the configured target size.

Error handling and retries: the code includes a retry/backoff mechanism inside fetchOne; Action-Plan recommends moving to exponential backoff with jitter and richer HTTP transport tuning.

### 2) Sidecar Generator (Generate-Sidecars.go)

Purpose: Emit per-crate-version JSON sidecar files reflecting metadata from crates.io-index. Can run independently of the actual .crate downloads.

Flags (from main()):
- -index-dir string: path to local crates.io-index (required)
- -out string: output directory for sidecar files (default out)
- -include-yanked: include yanked versions
- -limit int64: limit entries to write (0 = all)
- -concurrency int: worker count for processing index files (default ~16×CPU cores, min 64)
- -crates-base-url string: base URL for crate content (default https://static.crates.io/crates)
- -log-format text|json: structured logging format (default text)
- -log-level debug|info|warn|error: logging level (default info)
- -progress-interval duration: periodic progress logging interval (e.g., 5s; 0=disabled)
- -progress-every int: also log only when N new items processed (0=disabled)

Behavior:
- Walks the index tree (skipping .git, .github, .gitignore, config.json, README.md, *.keep) and concurrently processes files to write sidecars alongside the standard crate layout used by the downloader.
- Each sidecar path mirrors the crate layout and naming: <out>/<shard>/<shard2>/<name>-<vers>.crate.json.
- Sidecar content: full index line fields and helpful computed fields like crate_file, crate_url, and index_path (per README guidance). SSD/NVMe recommended due to many small writes.

### 3) Clone/Run Wrapper (Clone-Index.py)

Purpose: Convenience script to clone or update the crates.io-index and invoke the Go downloader with sensible defaults, supporting non-interactive runs for CI/automation.

Key functions:
- clone_or_update_index: clones rust-lang/crates.io-index if missing or runs git pull if present (with optional --skip-index-update).
- find_downloader: detects a local Download-Crates(.exe) or falls back to "go run Download-Crates.go" if Go is available.
- run_mirror_crates: maps wrapper args to downloader flags and executes.

Wrapper flags:
- --index-dir string (default C:\Rust-Crates\crates.io-index)
- --output-dir string (default C:\Rust-Crates\crates.io)
- --threads int → maps to -concurrency
- --skip-index-update
- --non-interactive / --yes
- --log-level [debug|info|warning|error]
- --log-path string
- --downloader-path string (explicit path to downloader binary)
- Deprecated compatibility flags retained: --rate-limit, --resume, --verify (no direct effect on downloader)

Logging: uses Python logging to stdout and optional file, no interactive prompt when --non-interactive is provided; exits non-zero on failure.

### 4) Archive-Hasher (Archive-Hasher/)

Purpose: Inventory a directory, compute a wide set of hashes, optionally sign results with GPG, generate a TOML report, and create a ZIP of the directory. Useful for packaging and attesting a completed mirror directory.

Features and notes:
- Hashes: KangarooTwelve, BLAKE3, SHA3-256, BLAKE2b, SHA-512; Whirlpool, RIPEMD-160, XXH3; plus SHA-256, XXHash64, Murmur3.
- GPG signing: relies on golang.org/x/crypto/openpgp (deprecated); Action-Plan recommends switching to a maintained fork (e.g., ProtonMail/go-crypto) or external gpg.
- Output: TOML with directory info, hashes, signature, and per-file inventory; creates a ZIP archive of the directory.
- Flags (see README): -dir (required), -gpgkey (optional), -verbose, -progress.

---

## Data Flow and File Layout

High-level flow:
1) Clone/update index (Clone-Index.py) → local crates.io-index directory.
2) Extract URLs and checksums (Download-Crates.go: readCratesFromIndex) → list of crate download URLs + optional SHA-256 map.
3) Download concurrently with HTTP/2 connection reuse, verify checksums when available, write to out/<shard>/<shard2>/<name>-<vers>.crate.
4) Log a JSONL manifest record per URL with timing, size, checksum, and error state.
5) Optional: Stream completed files into rolling tar.zst bundles (bundle-0000.tar.zst, ...) for more efficient storage.
6) Optional: Generate sidecar JSON metadata for each version (Generate-Sidecars.go) alongside the crate files.
7) Optional: Run Archive-Hasher on the final directory for inventory, hashes, signature, and a ZIP.

Sharding rules (both downloader and sidecars):
- If name length ≤3 → out/<name>/<name>-<vers>.crate.
- Else → first shard is the first character (or a special two-character prefix if the second char is '-') and the second shard is the next two characters following the first shard; files are placed under out/<first>/<next2>/.

Manifest JSONL example (fields):
```json
{"url":"https://static.crates.io/crates/serde/serde-1.0.147.crate","path":"D:\\Rust-Crates\\The-Crates\\s\\er\\serde-1.0.147.crate","size":123456,"sha256":"...","started_at":"2025-08-23T15:31:00Z","finished_at":"2025-08-23T15:31:01Z","ok":true}
```

---

## Build, Run, and Environment

Prerequisites:
- Go toolchain (README badge suggests Go ≥1.21; root go.mod currently specifies go 1.25)
- Git (for Clone-Index.py)
- Python 3.9+ if using the wrapper (Clone-Index.py)
- Windows, Linux supported (examples in README use PowerShell paths; adjust slashes accordingly)

Build examples:
- Downloader: `go build Download-Crates.go`
- Sidecars: `go build Generate-Sidecars.go`
- Archive-Hasher: build from Archive-Hasher directory (`go build`)

Run examples (PowerShell):
- Downloader (index mode):
  ```powershell
  .\Download-Crates -index-dir "D:\Rust-Crates\crates.io-index" -out "D:\Rust-Crates\The-Crates" -concurrency 256
  ```
- Downloader (with bundling):
  ```powershell
  .\Download-Crates -index-dir "D:\Rust-Crates\crates.io-index" -bundle -bundle-size-gb 8 -bundles-out bundles
  ```
- Wrapper:
  ```powershell
  python .\Clone-Index.py --index-dir "D:\Rust-Crates\crates.io-index" --output-dir "D:\Rust-Crates\The-Crates" --threads 256 --non-interactive --log-level info
  ```
- Sidecars:
  ```powershell
  .\Generate-Sidecars -index-dir "D:\Rust-Crates\crates.io-index" -out "D:\Rust-Crates\The-Crates" -concurrency 256 -log-format text -log-level info -progress-interval 5s -progress-every 500
  ```
- Archive-Hasher (PowerShell quoting caveats in its README):
  ```powershell
  go run Archive-Hasher\Archive-Hasher.go -dir 'D:\Rust-Crates\The-Crates'
  ```

Configuration tips:
- Concurrency should reflect available bandwidth and I/O; README reports ~6 hours for a full mirror on residential bandwidth.
- Use SSD/NVMe for sidecar generation due to many small writes.
- Consider enabling bundling to reduce filesystem overhead when storing millions of small files.

---

## Dependencies and Modules

Root module (go.mod):
- Module name: "Crates-Mirror" (note: not yet canonical import path)
- go 1.25
- Requires: github.com/klauspost/compress v1.18.0 (for zstd encoder)

Archive-Hasher module (Archive-Hasher/go.mod):
- Separate module; imports various hashing libraries:
  - github.com/cloudflare/circl/xof/k12
  - lukechampine.com/blake3
  - golang.org/x/crypto/{sha3, blake2b, ripemd160, openpgp}
  - github.com/jzelinskie/whirlpool, github.com/zeebo/xxh3, github.com/cespare/xxhash/v2, github.com/spaolacci/murmur3

Python wrapper: depends on system git and Python stdlib; no third-party modules required by current implementation.

Planned repo hygiene (Action-Plan): normalize module names/versions, consider go.work to clarify multi-module layout, add CI and build scripts.

---

## Security, Reliability, and Performance Considerations

- Integrity: SHA-256 verification when checksums are known from index or an external JSONL file; manifest logs outcomes for auditing.
- HTTP robustness: current code retries transient download failures; Action-Plan proposes exponential backoff with jitter and better classification of retryable errors.
- Resource usage: very high concurrency can stress file descriptors and I/O; plan includes bounded worker pools and improved progress/metrics.
- Signing (Archive-Hasher): uses deprecated openpgp package; migrate to a maintained library or call external gpg for signatures.
- Telemetry/Observability: planned Prometheus metrics and pprof endpoints for the downloader.

---

## Testing and CI Status

- Tests: none committed for core pathing, checksum verification, or sidecar layout at this time.
- Planned unit tests (Action-Plan/TODO): shard/path helpers, verifyFile behavior, bundler rotation, index parsing, and sidecar generation using a synthetic index under testdata/.
- CI: No workflows currently present. Plan to add GitHub Actions for build/test on Windows/Linux, linting (staticcheck/golangci-lint), vet, and race detector.

---

## Roadmap (from README + Action-Plan)

Near-term:
- Logging/progress unification across tools using log/slog with -log-format and -log-level flags
- Downloader: HTTP Transport tuning; improved retries; structured manifest schema with versioning
- Wrapper: fully non-interactive workflows (completed), clearer docs
- Core unit tests and initial CI

Mid-term:
- Downloader: Prometheus metrics and pprof endpoints; smarter resume (HTTP Range) where supported; adaptive concurrency
- Sidecars: batching/buffering writes or optional sidecar tar.zst to reduce inode churn
- Archive-Hasher: replace openpgp; add parallel hashing; tests for determinism and TOML schema

Long-term:
- Go rewrite of the index clone/update tool with robust progress and retries
- Pluggable storage backends (local FS, S3-compatible, Azure Blob) while maintaining layout
- End-to-end tests and benchmarks on subsets of the index

---

## Current Status (2025-08-23)

- Downloader and sidecar generator build and run locally; README provides comprehensive usage and examples.
- Wrapper updated: supports --non-interactive, uses Python logging, correctly invokes Go downloader (auto-detects binary or falls back to `go run`).
- Planning docs highlight next improvements: logging standardization, HTTP tuning, tests, CI, and metrics.
- Archive-Hasher functional but pending modernization of the signing path.

References:
- README.md — quick start, flags, performance rationale, sidecar usage
- Action-Plan.md — deep-dive on gaps and prioritized tasks
- TODO.md — actionable checklist with statuses
- Progress-Log.md — dated updates (entries on 2025-08-23)

---

## Glossary

- crates.io-index: Git repository with crate metadata stored as JSON lines per version.
- JSONL: JSON Lines; one JSON object per line.
- Sidecar: separate JSON file with metadata for a corresponding .crate file.
- Bundling: streaming files into a compressed tar archive (.tar.zst) to reduce filesystem overhead.

---

## Suggested Next Steps

1) Unify logging and progress across Go tools (log/slog) and add configurable progress intervals.
2) Add HTTP Transport tuning and exponential backoff with jitter; make retry parameters configurable.
3) Introduce unit tests for pathing, checksum verification, bundler rotation, and sidecar generation with a synthetic index.
4) Add CI (Windows + Linux) for build/test/lint.
5) Plan Archive-Hasher migration away from deprecated openpgp and add parallel hashing option.
