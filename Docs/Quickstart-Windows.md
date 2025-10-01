## Quickstart (Windows / PowerShell)

This guide gets you mirroring crates.io quickly on Windows using PowerShell.

### Prerequisites
- Go 1.25+ (system) or use the bundled toolchain under `.tools/go`.
- Python 3.9+
- Git

### Set up directories
```
$root = "$env:USERPROFILE\Rust-Crates"
New-Item -Force -ItemType Directory $root, "$root\crates.io-index", "$root\mirror" | Out-Null
```

### Clone crates.io-index (first time)
```
git clone https://github.com/rust-lang/crates.io-index "$root\crates.io-index"
```

For subsequent runs, update instead of re-clone:
```
git -C "$root\crates.io-index" fetch --prune --all
git -C "$root\crates.io-index" reset --hard origin/master
```

### Build CLIs
```
go build -o .\bin\download-crates.exe .\cmd\download-crates
go build -o .\bin\generate-sidecars.exe .\cmd\generate-sidecars
```

### Dry-run preflight (validate config, estimate work)
```
.\bin\download-crates.exe `
  -index-dir "$root\crates.io-index" `
  -out "$root\mirror" `
  -concurrency 256 `
  -dry-run `
  -log-level info
```

### Download mirror (with metrics on :9090)
```
.\bin\download-crates.exe `
  -index-dir "$root\crates.io-index" `
  -out "$root\mirror" `
  -concurrency 256 `
  -include-yanked `
  -progress-interval 5s `
  -listen :9090 `
  -log-format json
```

Metrics: http://localhost:9090/metrics
Status API: http://localhost:9090/api/status

### Generate sidecars
```
.\bin\generate-sidecars.exe `
  -index-dir "$root\crates.io-index" `
  -out "$root\mirror" `
  -concurrency 256 `
  -progress-interval 5s
```

### Tips
- Use fast storage (NVMe). Avoid mirroring on network shares.
- Consider disabling NTFS compression on `$root\mirror` for better throughput.
- Resume is automatic: existing verified files are skipped via checksum/size checks.

