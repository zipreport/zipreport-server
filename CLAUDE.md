# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ZipReport Server is an HTML-to-PDF conversion daemon. It receives ZIP archives (`.zpt` files) containing
HTML/CSS/JS/images, serves them via ephemeral HTTP servers, renders them in headless Chromium (via the `rod` library),
and returns the PDF output.

## Build & Test Commands

```bash
make build              # Build zipreport-server and browser-update binaries to bin/
make test-short         # Run tests that don't require Chrome (fast, CI-safe)
make test-integration   # Run full test suite including browser-based tests (requires Chrome)
make test-fixtures      # Regenerate .zpt fixtures from source HTML files
make fmt                # Format code
make lint               # Run golangci-lint and govulncheck
make docker             # Build Docker image
make certificate        # Generate self-signed TLS certificates
```

Run a single test:

```bash
go test -v -p 1 -run TestRenderEndpoint_Success ./test/...
```

Tests requiring Chrome will fail outside Docker/CI unless Chrome is installed with `--no-sandbox` capability. Use
`Dockerfile.test` to run the full suite locally:

```bash
docker build -f Dockerfile.test -t zipreport-test .
docker run --rm -v "$(pwd)/test:/app/test" zipreport-test
```

Tests use `-p 1` (sequential execution) to avoid port conflicts between test instances.

## Architecture

### Request Flow

1. `POST /v2/render` (multipart form with `.zpt` file) → `internal/apiserver/endpoints.go:renderAction`
2. `buildRenderJob()` parses form fields, creates `render.Job` with a `zpt.ZptReader` wrapping the uploaded ZIP
3. `render.Engine.RenderJob()` orchestrates:
    - Acquires a browser from `rod.Pool` (headless Chromium instances)
    - Acquires a `zpt.ServerPool` slot → starts ephemeral HTTP server serving ZIP contents on `localhost:PORT`
    - Navigates browser to `http://localhost:PORT/<script>` (default: `report.html`)
    - Either waits for `console.log("zpt-view-ready")` (JS event mode) or waits for page load + settling time
    - Calls `page.PDF()` and returns bytes
4. Response: `Content-Type: application/pdf` with raw PDF bytes

### Key Packages

- **`cmd/zipreport-server`** — Entry point. CLI flags: `-c config.json`, `-version`, `-sample-config`
- **`internal/`** — App lifecycle (`application.go`), config parsing (`config.go`), HTTP API (`apiserver/`)
- **`pkg/render/`** — `Engine` (browser pool + server pool + job execution), `Job`/`JobResult` structs, PDF options
- **`pkg/zpt/`** — `ZptReader` (ZIP reader wrapper), `ZptServer` (ephemeral HTTP server with path traversal protection),
  `ServerPool` (concurrent slot management)
- **`pkg/monitor/`** — Prometheus metrics definitions (counters, gauges, histogram)
- **`pkg/metrics/`** — Prometheus HTTP server configuration
- **`pkg/browser/`** — Local Chromium downloader with arm64 Linux support (replaces rod's built-in downloader)

### Concurrency Model

- Browser pool (`rod.Pool[rod.Browser]`) with configurable size (default 8)
- Server pool (`zpt.ServerPool`) with matching concurrency, ports starting at `baseHttpPort` (default 42000)
- Each render job acquires one browser + one server slot; both are returned to pool after completion
- Broken browsers (failed connections) are discarded rather than returned to pool

### Configuration

JSON config file with four sections: `apiServer`, `prometheus`, `zipReport`, `log`. See `config/config.sample.json` for
minimal config, `config/config.complete.json` for all options.

Key runtime settings in `zipReport`: `concurrency` (pool size), `baseHttpPort` (ephemeral server range start),
`enableConsoleLogging`, `enableMetrics`.

**Environment Variable Override**: `ZIPREPORT_API_KEY` overrides `apiServer.authTokenSecret`, useful for Docker
deployments without a config file mount.

### Test Structure

- `test/integration_test.go` — API endpoint tests (auth, validation, rendering)
- `test/security_test.go` — Path traversal, timeouts, concurrent requests
- `test/e2e_test.go` — End-to-end: JS event mode, multi-resource, multi-page, PDF validation
- `test/error_test.go` — Error scenarios: corrupt ZIP, missing files, empty uploads
- `test/concurrency_test.go` — Pool behavior under parallel load
- `test/metrics_test.go` — Prometheus counter/histogram verification
- `test/helpers_test.go` — PDF parsing utilities (page count, text search, validity check)
- `test/fixtures/` — `.zpt` files and their HTML sources

Tests share a single `monitor.Metrics` instance (avoids Prometheus re-registration panics) and use an incrementing port
counter to prevent conflicts.

### Docker Environment

The production Dockerfile uses a two-stage build: Go builder → Wolfi (`cgr.dev/chainguard/wolfi-base`) runtime with
Chrome dependencies. Wolfi is glibc-based, so the glibc Chromium that `browser-update` downloads runs unmodified.
Chrome runtime libs are pulled via `apk` (notably `libudev`, without which Chrome aborts at startup, and `libnss` for
Mozilla NSS — Wolfi's `nss` package is glibc Name Service Switch, not NSS). Chrome sandbox is disabled inside
containers (detected via `/.dockerenv` file). The `browser-update` tool downloads the correct Chromium version at
build time.

**Multi-platform support**: Docker images are built for `linux/amd64` and `linux/arm64`. The builder stage uses
`--platform=$BUILDPLATFORM` with cross-compilation (`CGO_ENABLED=0 GOOS/GOARCH`) to avoid slow QEMU emulation. Chromium
is sourced from different locations per architecture:

- **amd64**: Google's Chromium snapshots (matches rod's `RevisionDefault`)
- **arm64**: Playwright CDN (rod's built-in downloader lacks arm64 Linux builds)

## Module & Dependencies

Module: `zipreport-server` (Go 1.24.12)

Key dependencies: `gin-gonic/gin` (HTTP), `go-rod/rod` (Chrome automation), `oddbit-project/blueprint` (DI container +
HTTP server provider), `prometheus/client_golang` (metrics), `stretchr/testify` (testing).
