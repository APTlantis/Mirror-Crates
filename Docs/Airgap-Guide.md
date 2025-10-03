## Airgap Guide

This guide covers producing a portable crates mirror and verifying it offline.

### 1) Produce a manifest while downloading
Run the downloader to emit `manifest.jsonl`:
```sh
download-crates \
  -index-dir /data/crates.io-index \
  -out /data/crates-mirror \
  -concurrency 256 \
  -listen :9090 \
  -log-format json
```

The downloader writes `manifest.jsonl` at the repo root (or in `-out` if configured) with one JSON record per crate version, including path, size, and hash.

### 2) Package for transport (optional)
To reduce inode count and copy times, bundle into rolling archives:
```sh
download-crates \
  -index-dir /data/crates.io-index \
  -out /data/crates-mirror \
  -bundle \
  -bundles-out /data/crates-bundles
```

### 3) Generate sidecars
```sh
generate-sidecars \
  -index-dir /data/crates.io-index \
  -out /data/crates-mirror \
  -concurrency 256
```

### 4) Hash and inventory
```sh
go run ./Archive-Hasher/Archive-Hasher.go \
  -dir /data/crates-mirror \
  -out-dir /data/crates-artifacts \
  -progress-interval 10s \
  -hash-workers 8
```

Artifacts include multi-alg hashes and a YAML inventory; optionally sign with OpenPGP.

### 5) Move into the airgapped environment
Copy either the raw mirror directory or bundle archives and the manifest/artifacts. Use tools like `robocopy` (Windows) or `rsync` (Linux) to preserve timestamps and retry on transient errors.

### 6) Verify integrity offline
- Recompute hashes on arrival and compare against the inventory.
- Optionally re-run Archive Hasher to confirm end-to-end integrity.
- Spot-check against `manifest.jsonl` for a few crates to ensure paths and sizes align.

### Notes
- Manifest Schema: stable keys (name, version, path, size, sha256, yanked, timestamp).
- Backwards compatibility: include `schema_version` in the first record and bump on changes.
- Preservation: keep `crates.io-index` commit hash captured during mirroring for provenance.

