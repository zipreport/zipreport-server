# Changelog

All notable changes to ZipReport Server will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.3.2]

### Changed
- Updated Go toolchain from 1.24.12 to 1.26.3
- Switched Docker runtime base image from Ubuntu to Wolfi (`cgr.dev/chainguard/wolfi-base`); Chrome runtime
  dependencies are now installed via `apk` (requires `libudev` and Mozilla NSS via `libnss`)
- Rebased `Dockerfile.test` onto Wolfi with the `go-1.26` toolchain to match the runtime environment
- Updated all GitHub Actions to Node.js 24 compatible versions (checkout v6, setup-go v6, build-push-action v7, etc.)
- Replaced `CycloneDX/gh-gomod-generate-sbom` action with direct `cyclonedx-gomod` CLI (no Node.js 24 version available)
- Replaced source-based SBOMs with image-based SBOMs in Docker workflow, capturing OS packages and runtime dependencies
- Updated `golangci-lint-action` from v6 to v9 (golangci-lint v2 with Go 1.26 support)

### Fixed
- Unchecked error returns flagged by golangci-lint v2 `errcheck` across `cmd/`, `pkg/browser/`, `pkg/render/`, `pkg/zpt/`, and test files
- `staticcheck` QF1003: converted `runtime.GOOS` if/else chain to tagged switch in `pkg/browser/browser.go`

### Security
- Added Trivy container image vulnerability scanning to Docker workflow (fails build on fixable CRITICAL OS-level CVEs)
- Added Trivy SARIF upload to GitHub Security tab
- Added `security-events: write` permission for codeql-action
- Added weekly scheduled Docker image rebuilds to pick up base image and dependency patches
- Added `workflow_dispatch` trigger for manual Docker image rebuilds
- Updated `golang.org/x/net` v0.46.0 -> v0.54.0 (GO-2026-4918)
- Updated `github.com/jackc/pgx/v5` v5.7.6 -> v5.9.2 (CVE-2026-33816, CVE-2026-33815)
- Updated `filippo.io/edwards25519` v1.1.0 -> v1.2.0 (GO-2026-4503)
- Updated Go from 1.24.12 to 1.26.3 to resolve 12 stdlib vulnerabilities

## [2.3.1]

### Added
- Local Chromium download package (`pkg/browser`) with arm64 Linux support, replacing rod's built-in downloader which lacks arm64 builds
- Multi-platform Docker image support (`linux/amd64`, `linux/arm64`) for Apple Silicon and ARM64 servers
- `ZIPREPORT_API_KEY` environment variable override for `authTokenSecret` config option
- End-to-end rendering tests: JS event mode, JS timeout, multi-resource, multi-page, PDF content validation, all page sizes, landscape/portrait, custom margins
- Error scenario tests: corrupt ZIP, missing index file, zero timeout, empty upload, no file field
- Concurrency tests: parallel renders, exceed pool size, independent jobs with different page sizes
- Metrics validation tests: success/failure/total counters, conversion time histogram
- PDF validation helpers (`parsePDFPageCount`, `pdfContainsText`, `isValidPDF`) using raw byte scanning
- New test fixtures: `js-event.zpt`, `js-event-timeout.zpt`, `multi-resource.zpt`, `multi-page.zpt`, `missing-index.zpt`, `corrupt.zpt`
- Fixture source files alongside ZPTs for transparency
- `test/generate_fixtures.sh` script to regenerate ZPT fixtures from source
- `make test-fixtures` target
- Docker E2E test job in CI workflow
- `Dockerfile.test` for running full test suite locally without Chrome sandbox issues
- `CLAUDE.md` for Claude Code guidance

### Changed
- `cmd/browser-update` now uses local `pkg/browser` package instead of rod's `launcher.NewBrowser()`
- Dockerfile restructured for cross-compilation (`--platform=$BUILDPLATFORM` builder, `CGO_ENABLED=0` cross-compile)
- CI now runs `make test-integration` (full suite) instead of `make test-short`
- Test integration timeout increased from 5m to 10m to accommodate full suite
- `setupTestServer` context timeout increased from 30s to 120s (fixes pre-existing timeout failures in sequential page-size subtests)
- `github.com/prometheus/client_model` promoted from indirect to direct dependency (used by metrics tests)
- Updated Go toolchain to 1.24.12 (Dockerfile, Dockerfile.test, CI workflow, go.mod)

### Fixed
- Unchecked error returns flagged by golangci-lint in `pkg/zpt/pool.go`, test files
- gosimple issues: `time.Now().Sub()` → `time.Since()`, unnecessary `fmt.Sprintf`, redundant channel receive assignment
- Code formatting issues caught by `gofmt -s`

### Security
- Updated `github.com/quic-go/quic-go` v0.55.0 → v0.57.0 (GO-2025-4233)
- Updated Go from 1.24.7 to 1.24.12 to resolve 10 stdlib vulnerabilities

## [2.3.0]

### Added
- Comprehensive integration test suite with 17+ tests covering API endpoints, authentication, and security
- Security tests for path traversal protection
- Test fixtures and documentation in `test/` directory
- Configuration documentation in `docs/configuration.md`
- `make fmt` target for code formatting
- `make test`, `make test-integration`, and `make test-short` targets
- `.PHONY` declarations in Makefile for proper target handling
- Helper function `NewZptReaderFromFile()` for test support

### Changed
- Upgraded to Go 1.24
- Migrated to Blueprint framework for improved reliability
- Improved path traversal protection in HTTP file server
- Enhanced path sanitization with leading slash removal
- Updated authentication to use configurable header (`X-Auth-Key`)
- Refactored error handling with proper nil checks

### Fixed
- Path traversal vulnerability in ZPT server HTTP handler
- Race condition in server pool slot management
- Channel close race condition in JS event handling (using atomic operations)
- Nil pointer dereference in server pool when slots exhausted
- Error handling in browser connection (using `Connect()` instead of `MustConnect()`)
- Input validation for negative margin values
- Chrome sandbox error in Docker containers (automatic `--no-sandbox` when running in Docker)

### Security
- Added comprehensive path traversal protection with multiple validation layers
- Implemented proper path cleaning and validation in HTTP file server
- Added bounds checking for custom margin values (negative values rejected)
- Enhanced input validation for page sizes and margin styles
