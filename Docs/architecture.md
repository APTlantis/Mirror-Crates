# Mirror-Crates Architecture

This document explains the overall pipeline, data flow, file layouts, and the responsibilities of each component in the crates.io mirror.

---

## Overview

The mirror pipeline ingests the public crates.io index and downloads corresponding crate artifacts. It pairs each crate with a lightweight metadata sidecar, maintains a JSONL manifest for auditing/resume, and can bundle artifacts into rolling TAR archives. Separately, a directory hasher creates multi-algorithm checksums and a YAML inventory, and can package a TAR with embedded legacy TOML.

---

## Components

- Clone-Index (Python)
  - Clones/updates `crates.io-index` and invokes the Go downloader with sensible defaults.
  - Flags: `--non-interactive/--yes`, `--log-level`, `--downloader-path`, paths for index and output.

- Download-Crates (Go)
  - Scans a local `crates.io-index` to produce crate URLs + checksums, or reads a URL list.
  - Concurrently downloads artifacts with HTTP/2, retries with exponential backoff + jitter.
  - Writes a per-object JSONL manifest (`manifest.jsonl`). Optional bundling into rolling `*.tar.zst` archives.
  - Key flags: `-index-dir`, `-out`, `-concurrency`, `-include-yanked`, `-limit`, `-bundle`, `-bundles-out`, `-bundle-size-gb`, `-manifest`, `-checksums`, `-retries`, `-retry-base`, `-retry-max`, `-log-format`, `-log-level`, `-progress-interval`, `-progress-every`.

- Generate-Sidecars (Go)
  - Reads index files and writes per-version JSON metadata sidecars alongside the mirror layout.
  - Flags: `-index-dir`, `-out`, `-include-yanked`, `-limit`, `-concurrency`, `-crates-base-url`, `-log-format`, `-log-level`, `-progress-interval`, `-progress-every`.

- Archive-Hasher (Go)
  - Inventories a directory, streams data to multiple hashers, emits a YAML inventory and a TAR archive (with embedded legacy TOML).
  - Uses ProtonMail/go-crypto OpenPGP fork for signatures; supports deterministic parallel file reading with `-hash-workers`.
  - Flags: `-dir`, `-out-dir`, `-out-prefix`, `-gpgkey`, `-progress-interval`, `-log-format`, `-log-level`, `-verbose`, `-hash-workers`.

---

## Data Flow

1) Clone/Update Index:
   - `Clone-Index.py` ensures a local `crates.io-index` checkout.

2) URL + Checksum Expansion:
   - `Download-Crates` scans `indexDir` for JSONL entries and builds URLs like:
     `https://static.crates.io/crates/{name}/{name}-{vers}.crate`.
   - Index checksum (`cksum`) is used for verification when available.

3) Download + Verify:
   - High-concurrency HTTP/2 downloads with a tuned transport.
   - Retries: exponential backoff with jitter; retryable statuses (429/5xx) and transient net errors.
   - Verification: compute SHA-256; if index checksum is present, enforce equality.

4) Persist + Bundle (optional):
   - Files written into a deterministic shard layout (see Layouts below).
   - Manifest record appended per file: URL, path, size, sha256, timestamps, status, retries, error.
   - If bundling enabled, completed files are streamed into rolling `*.tar.zst` bundles with rotation by configured size.

5) Sidecars:
   - `Generate-Sidecars` writes `name-vers.crate.json` next to each shard path, capturing the original index line plus handy fields (`crate_file`, `crate_url`, `index_path`).

6) Hash + Package (optional):
   - `Archive-Hasher` produces a YAML inventory of the directory, and a TAR archive containing the directory and an embedded legacy TOML.

---

## File Layouts

Mirror artifact layout is derived from the crate name using two shard directories:

- If the name length  3: `outDir/<name>`
- Else:
  - `firstDir`: usually the first character; if the name starts with `1`, `2`, or `3`, use just that single digit (crates.io convention). For hyphen at position 1, include the first two characters.
  - `secondDir`: the next two characters after `firstDir` (clamped by name length).
  - Full path: `outDir/<firstDir>/<secondDir>/<file>`

Examples:
- `serde`  `s/er/serde-<vers>.crate`
- `ab` (length  3)  `ab/ab-<vers>.crate`
- `1serde`  `1/se/1serde-<vers>.crate`

Sidecars follow the same shard layout, with filenames like `name-vers.crate.json`.

Bundles (optional) are emitted under a separate output directory as `bundle-0000.tar.zst`, `bundle-0001.tar.zst`, ... and contain the downloaded files under paths prefixed with their host, e.g. `static.crates.io/serde-1.0.0.crate`.

---

## Manifest Schema (Downloader)

JSON Lines (`manifest.jsonl`), one record per attempted object:

- `schema_version` (int)
- `url` (string)
- `path` (string)
- `size` (int64)
- `sha256` (hex string)
- `started_at` (RFC3339)
- `finished_at` (RFC3339)
- `ok` (bool)
- `error` (string, optional)
- `retries` (int, optional)
- `status` (string, optional; e.g., `ok`, `error`)

Versioning: schema is now versioned via `schema_version`. Maintain backward-compatible evolutions when extending fields.

---

## Logging, Progress, and Retries

- All Go tools use structured logging via `log/slog` with `-log-format text|json` and `-log-level debug|info|warn|error`.
- Progress can be reported every N items or at fixed intervals with rate metrics where applicable.
- Downloader retries use exponential backoff with jitter, with configurable attempts and backoff windows.

---

## Observability (Planned)

- Prometheus metrics (requests, latencies, bytes, error codes) in the downloader.
- Optional `pprof` endpoints gated behind a flag for troubleshooting.
- A `-listen :9090` style flag to enable HTTP endpoints.

---

## CI and Tests

- GitHub Actions: builds and tests on Windows + Linux (Go 1.25.x), runs `staticcheck`, `golangci-lint`, and `go vet`, and executes unit tests (including `-race`).
- Unit test coverage includes pathing helpers, checksum verification, bundler rotation, index scanning, and sidecar generation flags.
- Synthetic index under `testdata/` validates index-driven features.

---

## Performance Guidance

- Start with moderate concurrency (e.g., 256) and adjust based on network and disk behavior.
- Use bundling to reduce inode churn on filesystems when writing millions of small files.
- SSD/NVMe recommended for heavy sidecar writes or massive mirroring runs.

---

## Security & Integrity

- Downloader verifies SHA-256 when index checksums are present; all files include computed SHA-256 in the manifest.
- Archive-Hasher supports OpenPGP signatures (ProtonMail/go-crypto fork) and writes a reproducible YAML inventory.

---

## Compatibility & Evolution

- When changing output schemas (manifest, sidecars, YAML), bump `schemaVersion` and document migration strategies.
- Keep logging defaults in text mode to avoid breaking existing tooling; JSON mode available for pipelines.
