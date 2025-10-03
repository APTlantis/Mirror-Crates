# Mirror Rust Crates

Mirror Rust Crates is a tooling suite for creating high‑fidelity mirrors of crates.io in hours rather than days. It targets air‑gapped and offline environments where a fresh Rust package mirror is needed quickly and repeatably.

<p align="center">
  <a href="https://github.com/APTlantis/Mirror-Rust-Crates/actions">
    <img alt="CI" src="https://img.shields.io/github/actions/workflow/status/APTlantis/Mirror-Rust-Crates/ci.yml?label=CI">
  </a>
  <a href="https://pkg.go.dev/github.com/APTlantis/Mirror-Rust-Crates">
    <img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/APTlantis/Mirror-Rust-Crates.svg">
  </a>
  <a href="https://goreportcard.com/report/github.com/APTlantis/Mirror-Rust-Crates">
    <img alt="Go Report Card" src="https://goreportcard.com/badge/github.com/APTlantis/Mirror-Rust-Crates">
  </a>
  <a href="https://github.com/APTlantis/Mirror-Rust-Crates/releases">
    <img alt="Release" src="https://img.shields.io/github/v/release/APTlantis/Mirror-Rust-Crates?include_prereleases">
  </a>
  <img alt="Go" src="https://img.shields.io/badge/Go-%3E%3D1.25-00ADD8?logo=go">
  <img alt="Python" src="https://img.shields.io/badge/Python-3.9%2B-3776AB?logo=python">
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/License-MIT-yellow.svg"></a>
  <img alt="PRs Welcome" src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg">
  <img alt="Status" src="https://img.shields.io/badge/status-active-success">
</p>

- Quick links: [Quickstart (Windows)](Docs/Quickstart-Windows.md) • [Airgap Guide](Docs/Airgap-Guide.md) • [Architecture](Docs/architecture.md)

## Table of Contents
- [Why It Is Fast](#why-it-is-fast)
- [Repository Layout](#repository-layout)
- [Getting Started](#getting-started)
  - [Prerequisites](#prerequisites)
  - [Build](#build)
  - [Wrapper Script](#wrapper-script)
  - [Downloader Usage](#downloader-usage)
  - [Prometheus and pprof](#prometheus-and-pprof)
  - [Sidecar Metadata Generator](#sidecar-metadata-generator)
  - [Archive Hasher](#archive-hasher)
- [Development](#development)
- [Windows and WSL Notes](#windows-and-wsl-notes)
- [Roadmap Highlights](#roadmap-highlights)
- [License](#license)

## Why It Is Fast

Traditional mirroring scripts struggle with millions of tiny `.crate` files. This project focuses on:
- Massive concurrency with HTTP/2 connection reuse.
- Incremental resume via checksum‑aware download verification.
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

```sh
go build ./cmd/...
```

Or build individually:

```powershell
go build -o bin\download-crates.exe .\cmd\download-crates
go build -o bin\generate-sidecars.exe .\cmd\generate-sidecars
```

Run without building:

```sh
go run ./cmd/download-crates -index-dir path/to/crates.io-index -out mirror-output
```

#### Wrapper Script

The Python wrapper defaults to user profile friendly paths:

```bash
python Clone-Index.py --index-dir "S:\\Rust-Crates\\crates.io-index" --output-dir "S:\\Rust-Crates\\crates.io" --threads 256 --non-interactive
```

It will clone or update the official `crates.io-index`, build or locate the downloader, and launch it with the provided thread count. All paths can be overridden via flags. Logging goes to `crate-download.log` inside the same root by default.

### Downloader Usage

```powershell
# With metrics on :9090
.\bin\download-crates.exe -index-dir "S:\Rust-Crates\crates.io-index" -out "S:\Rust-Crates\crates.io" -concurrency 256 -include-yanked -progress-interval 5s -listen :9090
```

Common options:
- `-limit` - Download only the first N entries for testing.
- `-bundle` / `-bundles-out` - Stream completed crates into rolling `tar.zst` archives.
- `-checksums` - Provide an external checksum JSONL file to enforce integrity.
- `-retries`, `-retry-base`, `-retry-max` - Configure retry policy.
- `-log-format`, `-log-level` - Structured logging (text or JSON).

### Prometheus and pprof

Expose metrics and runtime profiling by supplying `-listen :PORT`:
- Metrics: `http://localhost:PORT/metrics`
- pprof: `http://localhost:PORT/debug/pprof/`

### Sidecar Metadata Generator

```powershell
# If you built into .\bin as above
.\bin\generate-sidecars.exe -index-dir "S:\Rust-Crates\crates.io-index" -out "S:\Rust-Crates\crates.io" -concurrency 256 -include-yanked -progress-interval 5s -log-format text -log-level info
```

Sidecars (`crate-name-version.crate.json`) are written alongside the crate files using the same sharding scheme. A concurrency‑safe global limit ensures predictable output when using `-limit`.

### Archive Hasher

```sh
go run ./Archive-Hasher/Archive-Hasher.go -dir /data/crates-mirror -out-dir /data/crates-artifacts -progress-interval 10s -hash-workers 8
```

Generates multi‑algorithm hashes, emits a YAML inventory, can sign with OpenPGP, and creates a TAR that embeds legacy TOML metadata. See [Archive-Hasher/README.md](Archive-Hasher/README.md) for details.

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
