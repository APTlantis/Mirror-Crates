# Mirror-Crates — Progress Log

This log records updates made against TODO.md and related planning documents.

Conventions
- Timestamps are local unless specified.
- Each entry lists scope, changes, and next steps.
- Reference PR numbers/commit hashes when available.

---

### 2025-09-30 21:45
- Scope: Repository hygiene and downloader correctness.
- Changes:
  - Removed committed binaries, refreshed `.gitignore`, and fixed module paths (root + Archive-Hasher).
  - Corrected downloader retry accounting ensuring accurate Prometheus gauges and retry counts.
  - Introduced concurrency-safe limit handling for Generate-Sidecars and updated unit tests.
  - Mitigated Archive-Hasher tar walk file descriptor leaks.
  - Updated Clone-Index defaults to user-profile directories, ensured log directory creation, and removed global `chdir` usage.
- Next:
  - Restructure Go code into packages so `go test ./...` succeeds and prepare CI updates.

### 2025-09-30 22:05
- Scope: Documentation refresh.
- Changes:
  - Rewrote README with current instructions, ASCII-only formatting, and updated badge links.
  - Sanitised documentation files to remove non-ASCII artefacts.
  - Regenerated Action-Plan.md and TODO.md to reflect new priorities and completed items.
- Next:
  - Add architecture diagrams, HOWTO guides, and contributor docs as outlined in the updated plan.
