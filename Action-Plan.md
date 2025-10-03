# Mirror-Rust-Crates — Action Plan

Last updated: 2025-09-30

## Executive Summary
- Core tooling now has structured logging, resilient retries, Prometheus/pprof endpoints, and concurrency-safe limits.
- Repository hygiene improved (module paths fixed, binaries removed, sane defaults in Clone-Index.py).
- Next milestones focus on packaging shared libraries, full test coverage via `go test ./...`, and UI polish for GitHub appeal.

## Current Architecture Snapshot
1. **Clone-Index.py** — Clones/updates `crates.io-index`, provisions default directories under the user profile, and invokes the Go downloader.
2. **Download-Crates** — High-throughput HTTP/2 downloader with manifest logging, resumable checksum verification, optional bundling, and metrics.
3. **Generate-Sidecars** — Emits per-version JSON sidecars with concurrency-safe limit enforcement.
4. **Archive-Hasher** — Produces YAML inventories, multi-algorithm hashes, OpenPGP signatures, and TAR packages containing legacy TOML metadata.

## Recent Improvements
- Removed committed binaries and refreshed `.gitignore` to keep `go.sum` while excluding build artefacts.
- Added safe retry accounting and metrics handling in the downloader to avoid negative gauges and misreported retries.
- Introduced a thread-safe limit counter for sidecar generation, eliminating races detected by `-race` and ensuring deterministic output.
- Fixed Archive-Hasher file descriptor leaks during TAR packaging.
- Default Python wrapper paths now use `%USERPROFILE%/Rust-Crates`, create log directories automatically, and run `git pull` via `cwd` without global `chdir`.
- README rewritten with ASCII-only content, current flags, and up-to-date quickstarts.

## Open Issues & Opportunities
### Repository Structure
- `go test ./...` still fails because multiple binaries share the root `main` package. Need to move CLI entry points under `cmd/` and extract shared logic into reusable packages (`internal/downloader`, `internal/index`, etc.).
- Once refactored, update tests to import the shared packages and enable full-module CI coverage.

### Observability & UX
- Bundle Prometheus dashboards and example Grafana panels so operators can monitor throughput quickly.
- Publish sample manifests with tooling to diff index vs. mirror state.

### GUI Initiative
- Prototype a cross-platform desktop UI (Go + Fyne or web front-end) that: 
  1. Validates prerequisites (disk space, index freshness).
  2. Launches clone/download/sidecar jobs with progress bars fed from Prometheus endpoints.
  3. Visualises bandwidth, retries, and bundle creation.
  4. Provides post-run verification workflows (hash inventory, signature reporting).

### Documentation
- Expand the Architecture doc with diagrams (PNG/SVG committed) and a manifest schema appendix.
- Author HOWTO guides: "Mirror in 10 Minutes", "Periodic Delta Sync", and "Air-Gapped Restore".
- Add CONTRIBUTING, CODEOWNERS, and issue/PR templates aimed at outside contributors.

### Release Engineering
- Add a `cmd` build matrix to GitHub Actions and publish release assets (`Download-Crates`, `Generate-Sidecars`, `Archive-Hasher`).
- Introduce `make`/`just` targets for `build`, `test`, `lint`, `mirror-sample` to simplify onboarding.

## Next Five Actions
1. Refactor downloader and sidecar logic into packages so `go test ./...` passes; adjust tests accordingly.
2. Create GitHub Actions workflow(s) for the new layout, including race detector and lint jobs.
3. Produce architecture diagrams and update documentation with new defaults and metrics endpoints.
4. Draft CONTRIBUTING.md and CODE_OF_CONDUCT.md to encourage community participation.
5. Define GUI MVP requirements and technical stack decision, documenting it in `Docs/gui-roadmap.md`.

## Risk Notes
- Large refactors may introduce regressions in download performance; protect with benchmarks or integration tests on a small sample index.
- Moving binaries into `cmd/` requires updating existing automation scripts; coordinate documentation and wrapper changes simultaneously.
- GUI work may require additional dependencies; plan for vendoring/offline builds.
