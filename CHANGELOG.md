# Changelog

All notable changes to ZipReport Server will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- Docker base images are pinned by digest (bumped by Dependabot); apk packages still float
- The Chrome for Testing download is verified against a pinned SHA-256 and refused on mismatch
- Go binaries are built with `-trimpath -buildvcs=false` for reproducible output

## [3.0.0]

This release contains breaking changes; see the upgrade notes in `docs/migration-guide.md` ("Upgrading from 2.4.x to 3.0.0"). Entries marked **Breaking:** need action on upgrade.

### Added
- New `zipReport.egressAllow` (CIDRs or exact `host:port`) to permit intranet asset hosts through the egress proxy
- `enableHttpDebugging` now works (it was a no-op): configs that already set it to `true` will start emitting per-request Info lines

### Changed
- **Breaking:** rendered content no longer uses the container's `HTTP_PROXY`/`HTTPS_PROXY` (they are ignored); the per-job proxy dials directly, so deployments that depend on an outbound proxy lose external assets (chaining is a deferred follow-up). A Chrome started with a process-wide proxy switch (`--proxy-server`, `--proxy-pac-url`, `--no-proxy-server`) is refused
- **Breaking:** a failure to start the API or metrics listener (for example the port is in use) now exits the process with a non-zero status; previously it was logged and the process kept running
- **Breaking:** `timeout_job` now includes time spent waiting for a free render slot; a disconnected client cancels its job (the job stops at its next wait: slot wait, page creation, JS-event or settling wait, and its result reports `ErrJobCancelled`; navigation and PDF steps report the underlying context error); the API write deadline is re-armed after the upload to `timeout_job` + the configured `writeTimeout`. Known limits: a client that half-closes its socket after sending its request cancels its own job, and disconnect detection needs the request body to be read to EOF
- **Breaking:** the API `writeTimeout` now defaults to 720s (and the shipped sample changes from 600 to 720). The write deadline is re-armed after the upload to `timeout_job` + `writeTimeout`, so `writeTimeout` only needs to cover the upload (at least `readTimeout`); 600 still works. Previously the default was 60s when omitted, which cut renders over 60s. `readTimeout` defaults to 600s, `0` means default, and new 15s header and 60s idle timeouts apply. Explicit timeouts keep their values
- **Breaking:** the default `baseHttpPort` is now 22000 (was 42000). The old default lies inside the Linux ephemeral port range (32768-60999), so outbound sockets, including the per-job egress proxy's, could take a report port and fail the job. Configs that set `baseHttpPort` explicitly (including 42000) keep their value and log a startup warning when the render ports overlap the ephemeral range. Ephemeral report servers now also bind synchronously and skip a held port in favour of the next slot
- **Breaking:** `zipReport.readTimeoutSeconds` and `zipReport.writeTimeoutSeconds` now apply to the per-job report servers (they were ignored before)
- **Breaking:** the bundled browser moves from Chromium ~131 (snapshot revision 1321438) to Chrome for Testing 156.0.8078.47 on all architectures, and site isolation is re-enabled (rod's defaults disabled it). Expect higher memory use per job (site isolation runs more renderer processes). Cross-site iframes now run out of process, so their `console.log` output, including `zpt-view-ready`, is no longer seen by the engine; a report that signals readiness from a cross-site iframe will hit the JS timeout, so signal from the top-level page instead
- The `bin=` option of the `rod` environment variable is ignored; the browser is always the pinned build, resolved once at startup
- Updated `blueprint` to v0.16.0 and `blueprint/provider/httpserver` to v0.11.0
- Graceful stop now drains in-flight renders for up to 8 seconds (SIGINT/SIGTERM); the render engine is shut down after the API server stops accepting requests
- Every log line now includes a `hostname` field (`log.includeHostname`, default `true`, was previously ignored); set it to `false` to drop it
- Default `log.callerSkipFrames` is now 3 (Blueprint default)
- TLS: with `tlsEnable`, an incomplete `tlsCert`/`tlsKey` pair now aborts startup; the mTLS `tlsAllowedDNSNames` check, which was inverted before Blueprint v0.12, now works as documented
- Podman and Kubernetes containers are detected like Docker (`--no-sandbox`, shared browser)
- Updated `prometheus/client_golang` to v1.25.0, `prometheus/client_model` to v0.6.3 and `stretchr/testify` to v1.12.1

### Fixed
- A render whose browser context could not be disposed no longer leaves its egress proxy open forever: the proxy cuts its live connections at once and is closed once the context is disposed or the browser is discarded; if disposal keeps failing on a live browser it stays open in deny-all mode (bounded to that job)
- The job deadline is now honoured while waiting in js_event mode and during the settling wait
- The uploaded report file is now closed when the request ends

### Security
- Each render job's browser traffic now goes through a per-job egress proxy that refuses loopback, local-interface and private destinations (including names that resolve to them, link-local and cloud metadata). Rendered pages can no longer reach other jobs' servers, the API, the metrics endpoint or the internal network. Residual risks are listed in the README
- Bumped Go toolchain from 1.26.4 to 1.27.3, resolving 16 reachable standard-library advisories (net/http, crypto/tls, crypto/x509, net/url, net/textproto, encoding/asn1); golang.org/x/net is now v0.60.0
- `tlsKeyPasswordEnvVar` is no longer cleared after it is read (Blueprint v0.12+), so Chromium child processes inherit it; prefer `tlsKeyPasswordFile`
- **Breaking:** the server refuses to start when `authTokenSecret` / `ZIPREPORT_API_KEY` is empty, shorter than 32 characters, padded with whitespace, or a placeholder value published in earlier sample configs and docs. The Docker image no longer ships a usable token
- `NewApiServer` returns an error instead of disabling authentication when given an empty secret
- Documentation examples generate secrets with `openssl rand -hex 32`; the CI e2e job generates its key per run
- Render jobs run in isolated, disposable browser contexts, and `ignore_ssl_errors` is scoped to the job's page. Previously one job's storage, cookies or service workers, and its certificate override, could affect other jobs. The browser HTTP cache no longer persists between jobs; worker/service-worker requests are not covered by `ignore_ssl_errors` (see README)
- Zip entries are now streamed and size-checked while they are read, and requests whose `Host` header does not match the report server are rejected with 421

## [2.4.1]

### Changed
- Restructured GitHub Actions to the shared Go layout: CI split into `lint`/`test`/`build`/`govulncheck` jobs (plus the existing Docker build/e2e) gated by a `ci-success` job; the Go version is now pinned via `go-version-file: go.mod` instead of a hardcoded value
- Added `pr.yml` (verifies `go.mod`/`go.sum` are tidy), `release.yml` (tag `v*` → GitHub release with generated notes), `sbom.yml` (CycloneDX + SPDX SBOMs on release, scanned with Grype, signed with Cosign, uploaded to the release and Dependency-Track), and `dependabot.yml` (Go modules + GitHub Actions)

### Security
- Bumped Go toolchain from 1.26.3 to 1.26.4 to resolve standard-library advisories GO-2026-5037 (`crypto/x509`) and GO-2026-5039 (`net/textproto`)

## [2.4.0]

### Changed
- Switched the Docker runtime base image from Ubuntu to Wolfi (`cgr.dev/chainguard/wolfi-base`); Chrome runtime dependencies are now installed via `apk` (notably `libudev`, required at startup, and Mozilla NSS via `libnss`)
- Rebased `Dockerfile.test` onto Wolfi with the `go-1.26` toolchain to match the runtime environment
- Updated all Go module dependencies to their latest versions (gin, rod, prometheus, redis, quic-go, zerolog, blueprint, golang.org/x/*, etc.)
- Migrated `blueprint/provider/httpserver` to v0.9.x. **Breaking config change**: the `apiServer.options` block is removed — set `apiServer.authTokenHeader`, `apiServer.authTokenSecret`, and `apiServer.defaultSecurityHeaders` (bool) at the `apiServer` level instead
- Changed the scheduled Docker image rebuild from weekly to daily

### Fixed
- Browser pool permanently leaked a slot on every browser/connection error, eventually hanging the server after `concurrency` failures; failed slots are now returned to the pool
- Prometheus metrics endpoint was created but never started when `enableMetrics` was enabled
- Render engine (browser pool, ephemeral servers, shared Chrome) was never shut down on exit; now registered as a destructor
- In shared no-sandbox mode, a single failed render could close the shared Chrome for all concurrent jobs; the shared browser is no longer closed on discard and is relaunched if it dies
- Pinned the pre-installed Chromium binary so rod no longer deletes and re-downloads it at runtime on the first render
- HTTP file server now matches on the decoded URL path instead of the raw request URI, so query strings on asset URLs no longer break lookups
- Removed unused/uninitialized metrics fields and wired the `current_browsers` gauge
- Default `IgnoreSSLErrors` for a new job aligned to `false` (matching the API default)
- Unknown file extensions now serve `application/octet-stream` instead of an empty `Content-Type`

### Security
- Capped decompressed zip-entry size and request body size (128 MiB each) to prevent zip-bomb and oversized-upload denial of service
- Bounded client-supplied render timeouts and settling time to prevent a client from holding a pool slot indefinitely
- API auth token is now compared in constant time

## [2.3.2]

### Changed
- Updated Go toolchain from 1.24.12 to 1.26.3
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
