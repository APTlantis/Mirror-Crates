# Mirror Rust Crates

Mirror Rust Crates is a tooling suite for creating high-fidelity mirrors of crates.io in hours rather than days. It targets air-gapped and offline environments where a fresh Rust package mirror is needed quickly and repeatably.

## Badges

![Go](https://img.shields.io/badge/Go-%3E%3D1.25-00ADD8?logo=go)
![Python](https://img.shields.io/badge/Python-3.9%2B-3776AB?logo=python)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/APTlantis/Mirror-Rust-Crates/ci.yml?label=CI)](https://github.com/APTlantis/Mirror-Rust-Crates/actions)
![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)
![Status](https://img.shields.io/badge/status-active-success)

## Why It Is Fast

Traditional mirroring scripts struggle with millions of tiny `.crate` files. This project focuses on:
- Massive concurrency with HTTP/2 connection reuse.
- Incremental resume via checksum-aware download verification.
- Optional bundling into rolling `tar.zst` archives to reduce inode churn.
- Structured JSONL manifests for auditing and restart safety.
- Prometheus metrics and pprof endpoints for visibility under load.

## Repository Layout

```
Clone-Index.py               Python wrapper: fetch crates.io-index and invoke Go CLIs
cmd/download-crates/         CLI: high-performance crate downloader
cmd/generate-sidecars/       CLI: generate per-crate metadata sidecars
internal/downloader/         Download, retry, sharding, and optional bundling engine
internal/sidecar/            Sidecar generation library reused by the CLI
Archive-Hasher/              Directory hashing and packaging utility
Docs/                        Architecture and deep-dive documentation
Testdata/                    Synthetic fixtures used in unit tests
```

## Getting Started

### Prerequisites
- Go 1.25 or newer
- Python 3.9 or newer
- Git (for cloning the crates.io index)

See also: Docs/Quickstart-Windows.md and Docs/Airgap-Guide.md.

### Build

Build all CLIs (recommended):

```
go build ./cmd/...
```

Or build individually:

```
go build -o bin\download-crates.exe ./cmd/download-crates
go build -o bin\generate-sidecars.exe ./cmd/generate-sidecars
```

Run without building:

```
go run ./cmd/download-crates -index-dir path/to/crates.io-index -out mirror-output
```

#### Wrapper Script

The Python wrapper defaults to user profile friendly paths:

```
python Clone-Index.py \
  --index-dir "%USERPROFILE%\Rust-Crates\crates.io-index" \
  --output-dir "%USERPROFILE%\Rust-Crates\mirror" \
  --threads 256 \
  --non-interactive
```

It will clone or update the official `crates.io-index`, build or locate the downloader, and launch it with the provided thread count. All paths can be overridden via flags. Logging goes to `crate-download.log` inside the same root by default.

### Downloader Usage

```
download-crates \
  -index-dir /data/crates.io-index \
  -out /data/crates-mirror \
  -concurrency 256 \
  -include-yanked \
  -progress-interval 5s \
  -listen :9090
```

Common options:
- `-limit` � Download only the first *N* entries for testing.
- `-bundle` / `-bundles-out` � Stream completed crates into rolling `tar.zst` archives.
- `-checksums` � Provide an external checksum JSONL file to enforce integrity.
- `-retries`, `-retry-base`, `-retry-max` � Configure retry policy.
- `-log-format`, `-log-level` � Structured logging (text or JSON).

### Prometheus and pprof

Expose metrics and runtime profiling by supplying `-listen :PORT`:
- Metrics: `http://localhost:PORT/metrics`
- pprof: `http://localhost:PORT/debug/pprof/`

### Sidecar Metadata Generator

```
generate-sidecars \
  -index-dir /data/crates.io-index \
  -out /data/crates-mirror \
  -concurrency 256 \
  -progress-interval 5s
```

Sidecars (`crate-name-version.crate.json`) are written alongside the crate files using the same sharding scheme. A concurrency-safe global limit ensures predictable output when using `-limit`.

### Archive Hasher

```
go run ./Archive-Hasher/Archive-Hasher.go \
  -dir /data/crates-mirror \
  -out-dir /data/crates-artifacts \
  -progress-interval 10s \
  -hash-workers 8
```

Generates multi-algorithm hashes, emits a YAML inventory, signs with OpenPGP, and creates a TAR that embeds legacy TOML metadata.

## Development

- Format: `gofmt -w .` (exclude vendored toolchain: `git ls-files -z | rg -z -v "^\.tools/" | %{ $_ } | ForEach-Object { $_ }`)
- Tests: `go test ./...` (unit tests live under `internal/`)
- Lint suggestions: `go vet ./...`, `staticcheck ./...`, `golangci-lint run ./...`

### Windows and WSL Notes

- PowerShell examples use `bin\*.exe`; on WSL/Linux use `bin/*`.
- The repo includes a local Go toolchain under `.tools/go` for reproducible builds. If preferred, use your system Go 1.25+.
- Large runs benefit from fast disks (NVMe) and NTFS compression disabled on the destination directory.

## Roadmap Highlights

- Split shared logic into internal packages for better reuse and coverage.
- Add GUI front-end for monitoring progress, pre-flight checks, and bundle verification.
- Publish curated sample manifests and disk usage estimators for planning offline mirrors.

## License

MIT License. See [LICENSE](LICENSE).
