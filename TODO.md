# Project TODO – derived from Action-Plan.md

Generated: 2025-09-30
Source: Action-Plan.md (Last updated: 2025-09-30)

Legend: [ ] not started | [x] done | [~] in progress

---

## Structural Refactor
- [ ] Move CLI entry points into `cmd/` directories and extract shared logic into reusable packages.
- [ ] Update tests to exercise new packages and ensure `go test ./...` passes.
- [ ] Add integration test harness using synthetic index fixtures.

## CI and Tooling
- [ ] Update GitHub Actions workflows for new layout (build, test, lint, race).
- [ ] Publish release artifacts (binaries + checksums) on tagged builds.
- [ ] Add `Makefile` or `justfile` targets for build/test/lint/mirror-sample.

## Documentation
- [x] Rewrite README with ASCII-only content and current instructions.
- [ ] Expand `Docs/architecture.md` with diagrams and manifest schema appendix.
- [ ] Add HOWTO guides (quick mirror, delta sync, air-gapped restore).
- [ ] Create CONTRIBUTING.md, CODEOWNERS, and issue/PR templates.
- [ ] Document GUI roadmap in `Docs/gui-roadmap.md`.

## Observability & UX
- [ ] Provide Grafana dashboard examples for metrics exposed by the downloader.
- [ ] Supply sample manifests and diff tooling documentation.

## GUI Initiative
- [ ] Decide on GUI stack and MVP scope.
- [ ] Prototype GUI wiring to downloader metrics (progress, retries, throughput).

## Release Readiness
- [x] Remove committed binaries and clean `.gitignore`.
- [x] Fix module paths for root and Archive-Hasher modules.
- [x] Ensure Clone-Index defaults are portable and non-interruptive.
- [x] Harden downloader retries and metrics behaviour.
- [x] Make sidecar limits concurrency-safe.
- [x] Prevent Archive-Hasher file descriptor leaks during tar packaging.

---

Notes
- Run targeted tests for each tool until repo restructuring enables `go test ./...`.
- Keep documentation ASCII-only to avoid rendering artefacts in terminals and GitHub UI.
- When refactoring packages, update the Python wrapper to call the relocated binaries or shared library.
