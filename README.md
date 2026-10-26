# zipreport-server

[![Go Version](https://img.shields.io/github/go-mod/go-version/zipreport/zipreport-server)](https://go.dev/)
[![License](https://img.shields.io/github/license/zipreport/zipreport-server)](https://github.com/zipreport/zipreport-server/blob/master/LICENSE)
[![CI](https://github.com/zipreport/zipreport-server/actions/workflows/ci.yml/badge.svg)](https://github.com/zipreport/zipreport-server/actions/workflows/ci.yml)
[![Release](https://github.com/zipreport/zipreport-server/actions/workflows/release.yml/badge.svg)](https://github.com/zipreport/zipreport-server/actions/workflows/release.yml)

ZipReport-server is the [zipreport](https://github.com/zipreport/zipreport) HTML to PDF conversion daemon, based on
[rod](https://github.com/go-rod/rod) and Chromium, written in Go.

**Note:** zipreport-server 2.x and later require the zipreport library version 2.0.0 or newer, and are
**incompatible** with earlier versions.

## Upgrading

### Upgrading to 3.0.0

Version 3.0.0 has breaking changes. The full list, with what to change, is in the
[migration guide](./docs/migration-guide.md); in short:

1. **API secret requirements.** The server refuses to start when `authTokenSecret` (or `ZIPREPORT_API_KEY`) is empty,
   shorter than 32 characters, padded with whitespace, or one of the placeholder values that appeared in earlier sample
   configs and docs. The Docker image no longer contains a usable secret. Generate one with `openssl rand -hex 32` and pass
   it as `ZIPREPORT_API_KEY` or in your mounted config. Images tagged `latest` pick up this change as soon as 3.0.0 is
   released.
2. **HTTP(S)_PROXY.** Rendered pages no longer use the container's `HTTP_PROXY`/`HTTPS_PROXY` (they are ignored). All of a
   job's browser traffic goes through a per-job egress proxy that dials directly (no chaining), so deployments that depend
   on an outbound proxy lose external assets (CDN fonts, scripts, images). Loopback, private and link-local destinations are
   blocked unless allowed with `zipReport.egressAllow`. A Chrome started with `--proxy-server`, `--proxy-pac-url` or
   `--no-proxy-server` is refused.
3. **Listener failures exit.** If the API or metrics listener cannot start (for example the port is in use), the process
   now exits instead of logging and carrying on.
4. **`baseHttpPort` default is 22000** (was 42000, inside the Linux ephemeral port range). A configured range that
   overlaps the ephemeral range logs a startup warning.
5. **API timeouts.** `apiServer.writeTimeout` defaults to 720s, header read is limited to 15s and idle keep-alive to 60s;
   a `readTimeout`/`writeTimeout` of 0 or less means the default. `timeout_job` now includes the wait for a free render
   slot, and a client that disconnects cancels its job.
6. **Chrome for Testing 156 with site isolation.** Expect higher memory per job; a `zpt-view-ready` `console.log` from a
   cross-site iframe is no longer seen, so signal readiness from the top-level page.
7. **`enableHttpDebugging` now works.** It was a no-op; configs that set it to `true` start logging one Info line per
   report-server request (capped per job).
8. **`zipReport` read and write timeouts now apply.** `readTimeoutSeconds`/`writeTimeoutSeconds` (default 300 each) were
   ignored; slow assets that took longer previously succeeded and may now fail, so raise them if needed.
9. **Go 1.27.3** is required to build from source.
10. **Images are published only for releases.** Docker images are pushed only when a `v*` release tag is created:
    `latest` is the latest release (no longer the default branch), and the `master`/`main` image tags are no longer
    updated. Pin `<version>`, `<major>.<minor>` or `<major>` (e.g. `3.0.0`, `3.0`, `3`) instead of a branch tag.

### Upgrading to 2.4.0

**Breaking change — configuration format.** The `apiServer.options` block has been removed. Its keys are now set
directly on `apiServer`.

Before (2.3.x and earlier):

```json
{
  "apiServer": {
    "options": {
      "authTokenHeader": "X-Auth-Key",
      "authTokenSecret": "<generate: openssl rand -hex 32>",
      "defaultSecurityHeaders": "1"
    }
  }
}
```

After (2.4.0 and later):

```json
{
  "apiServer": {
    "authTokenHeader": "X-Auth-Key",
    "authTokenSecret": "<generate: openssl rand -hex 32>",
    "defaultSecurityHeaders": true
  }
}
```

Note that `defaultSecurityHeaders` is now a boolean (`true`/`false`) rather than the string `"1"`. The server refuses
to start if `authTokenSecret` is empty. If you set the key only via the `ZIPREPORT_API_KEY` environment variable, no
config change is required. The Docker base image also moved from Ubuntu to Wolfi; behavior is unchanged.

### Upgrading to 2.3.0

Starting with v2.3.0, zipreport-server uses an external configuration file and no longer bundles a self-signed certificate;
instead, mount a folder with a proper config.json (see [the sample configuration file](./config/config.sample.json)), and
optionally modify and use [the sample script](./config/generate_certs.sh) to generate your own self-signed certificates;
see below for a docker-compose example, and check the [migration guide](./docs/migration-guide.md).

Also, please note that Prometheus metrics are now exposed using a dedicated port, and metrics are disabled by default. Check
the [configuration file options](./docs/configuration.md) for available configuration parameters.

## Security considerations

zipreport-server relies on Chrome to render artifacts into PDF. It runs any scripts your template contains, and pages
may load public external resources (CDN fonts, scripts, images); internal destinations are blocked by the per-job
egress proxy described below. This may still pose a security risk in some environments. The daemon also creates
ephemeral HTTP servers on localhost as part of the rendering process.

- Each render job runs in a fresh browser context. Cookies, storage, service workers and the HTTP cache do not
  survive between jobs, so remote assets (CDN fonts, scripts, images) are downloaded again for every job.
- With `js_event=false`, the PDF is printed after page load plus the fixed `settling_time`. A slower remote asset
  can therefore be missing from the output with no error. Reports that depend on remote assets should use
  `js_event=true` and signal `zpt-view-ready` once their assets have loaded, or raise `settling_time`.
- Chrome runs with site isolation enabled, which separates cross-site frames into their own processes. This protects
  only when Chrome's sandbox is on. Chrome runs with `--no-sandbox` in containers (Docker, Podman, Kubernetes) and when
  `CI=true` or `GITHUB_ACTIONS=true`, so keeping the bundled Chrome version current is the protection there. Site isolation also runs more renderer processes
  per job, so container memory limits may need raising.
- Each job's browser traffic, loopback included, goes through a per-job egress proxy. It resolves names itself and
  refuses a destination if any resolved address is loopback, link-local, private, otherwise non-public, or belongs to
  a local interface. Public hosts, the job's own server and hosts permitted by `zipReport.egressAllow` (CIDRs or
  exact `host:port`) work. No `egressAllow` entry can open, on loopback and local addresses, other jobs' render
  ports (the job's own server is reached through its `localhost:PORT` name), the job's own proxy port, the API port
  or, if enabled, the metrics port. Allowing loopback ranges (e.g. `127.0.0.0/8`) still exposes other local services,
  including other jobs' egress proxies and Chrome's DevTools port. An entry covering Kubernetes Service/pod or Docker
  bridge CIDRs can reach this or other replicas' API and metrics ports through non-local addresses, which the
  hard-deny list does not cover. If the page does not reach its own server through the proxy, the job fails.
- Chrome's own background requests (for example `www.google.com:443`) pass through the same proxy; they are refused
  and counted in the "blocked network requests" warning logged per job.
- Residual risks of the egress proxy:
  - **WebRTC: pages can send STUN binding requests over UDP to arbitrary host:port, including internal addresses,
    outside the egress proxy (Chrome 156 ignores --force-webrtc-ip-handling-policy here). Only a network-level egress
    policy (Docker network rules, Kubernetes NetworkPolicy, host firewall) closes this.**
  - DNS prefetch and preconnect resolve names through the server's resolver (the query leaks, nothing is fetched),
    and the proxy's own lookups are a DNS channel for data leaving the server.
  - AIA certificate fetches and WebTransport (HTTP/3 over UDP) may not use the proxy.
  - Strict deployments still need a network-layer egress policy.
- The proxy listens on an ephemeral `127.0.0.1` port. The default `baseHttpPort` (22000) lies below the Linux ephemeral
  range (32768-60999); if you set it inside that range, reserve the render ports with
  `sysctl net.ipv4.ip_local_reserved_ports=<baseHttpPort>-<baseHttpPort+concurrency-1>` to avoid bind clashes.
- `ignore_ssl_errors` applies only to the report page's own frames. Requests made by service workers or web
  workers are always certificate-checked.

## How it works

The zipreport-server API receives a rendering request with an associated ZPT resource. For each rendering request,
zipreport-server launches an internal HTTP server to serve the ZPT content, and then instructs a Chrome instance to open
the temporary URL and render it to PDF.

The PDF is printed either after the page load event plus a settling time in milliseconds (the default behavior), or
once the page writes `zpt-view-ready` to the JS console (or `timeout_js` expires). With the console approach, a page
that signals after drawing its dynamic content (such as canvas elements) gets that content in the PDF.

## Available endpoints

### [POST] /v2/render

**Format:** multipart/form-data

**Fields:**

| Field             | Mandatory | Description                                                   |
|-------------------|-----------|---------------------------------------------------------------|
| report            | Yes       | Report file                                                   |
| page_size         | Yes       | Page size, case-sensitive (A5/A4/A3/Letter/Legal/Tabloid)     |
| margins           | Yes       | Margin type: none (0), minimal (0.2in), standard (0.4in), custom |
| margin_left       | No        | Margin in inches, used only if margins=custom (default 0)     |
| margin_right      | No        | Margin in inches, used only if margins=custom (default 0)     |
| margin_top        | No        | Margin in inches, used only if margins=custom (default 0)     |
| margin_bottom     | No        | Margin in inches, used only if margins=custom (default 0)     |
| landscape         | No        | If true, print in landscape                                   |
| script            | No        | Main html file (default report.html)                          |
| settling_time     | No        | Settling time, in ms (default 200ms, see below)               |
| timeout_job       | No        | Job timeout, in seconds (default 120s, see below)             |
| timeout_js        | No        | JavaScript event timeout, in seconds (default 30s, see below) |
| js_event          | No        | If true, wait for the javascript event (see below)            |
| ignore_ssl_errors | No        | If true, ssl errors in referenced resources will be ignored   |

**Limits**

Out-of-range values are clamped, not rejected: `settling_time` 0-30000 ms, `timeout_job` 1-600 s, `timeout_js`
1-300 s (so `timeout_job=0` means 1 s, not the default). A value that is not an integer (including one too large
for a 64-bit integer) falls back to the default. Booleans accept `1, t, T, TRUE, true, True` and
`0, f, F, FALSE, false, False`; anything else is treated as the default. The `margin_*` values must be finite
and non-negative; a non-numeric, non-finite or negative value is rejected with 400 whatever the margin type. With
`margins=custom`, left+right must not exceed the page width and top+bottom the page height (as oriented), otherwise 400.
Request bodies over 128 MiB are rejected with 413.

**settling_time** (default: 200)

Value in ms to wait after the page load event before printing the report. This setting is ignored if
js_event is enabled.

**timeout_job** (default 120)

Total time, in seconds, for the job: waiting for a free render slot, loading the page, settling_time or the
JavaScript event wait, and printing. A job that exceeds it fails.

**timeout_js** (default 30)

Time to wait, in seconds, for the javascript event to be triggered, before generating the
report anyway. Requires js_event to be true. If `timeout_job` runs out first, the job fails instead.

**js_event**

If true, the system will wait up to timeout_js for a specific console message before
generating the PDF. This allows for dynamic pages to signal when DOM manipulation is finished.

Triggering example:

```html
<script>
  (function () {
    // signal end of composition
    console.log("zpt-view-ready");
  })();
</script>
```

## Optional metrics endpoint (disabled by default)

### [GET] /metrics

Prometheus metrics endpoint, enabled with `zipReport.enableMetrics`. In Docker, set `prometheus.host` to `0.0.0.0` in
your mounted config; see [docs/configuration.md](./docs/configuration.md) for exposure caveats.

Besides the default internal Go metrics, the following are provided:

| metric                | type      | description                                                          |
|-----------------------|-----------|----------------------------------------------------------------------|
| total_requests        | counter   | Authenticated conversion requests (including ones rejected with 400/413) |
| total_request_success | counter   | Conversions that returned a PDF                                      |
| total_request_error   | counter   | Conversions that failed during rendering (excludes 400/401/413)      |
| conversion_time       | histogram | Elapsed conversion time histogram, in seconds. Buckets from 1 s to 600 s (the maximum job timeout); only successful conversions are observed. |
| current_http_servers  | gauge     | Current internal HTTP server count                                   |
| current_browsers      | gauge     | Browser connections in the pool (in containers they share one Chrome process) |

## Authentication

zipreport-server performs header-based authentication using the token specified in your configuration file (`apiServer.authTokenSecret`). Clients must pass the authentication token in the header named by `apiServer.authTokenHeader` (default `X-Auth-Key`).

### Environment Variable Override

The API key can also be set via the `ZIPREPORT_API_KEY` environment variable, which takes precedence over the config file. This is useful for Docker deployments:

```shell
# generate the key once and keep it: clients send it as the X-Auth-Key header
export ZIPREPORT_API_KEY="$(openssl rand -hex 32)"
docker run -p 6543:6543 -e ZIPREPORT_API_KEY ghcr.io/zipreport/zipreport-server:3.0.0
```

Example:

```shell
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: $ZIPREPORT_API_KEY" \
  -F "report=@report.zpt" \
  -F "page_size=A4" \
  -F "margins=standard" \
  -o output.pdf
```

Without authentication, requests will receive `401 Unauthorized`.

## Running with Docker

Starting with version 2.3.0, zipreport-server uses a configuration file. The Docker image includes a default configuration, so you can run with just the `ZIPREPORT_API_KEY` environment variable for basic usage. Without `ZIPREPORT_API_KEY` (or a mounted config with a compliant `authTokenSecret`), the container refuses to start. For advanced configuration, mount a custom config file to `/app/config/config.json`.

### Multi-Platform Support

Docker images are built for both `linux/amd64` and `linux/arm64`. This enables running on:
- Intel/AMD servers and desktops (amd64)
- Apple Silicon Macs — M1/M2/M3/M4 (arm64)
- ARM64 cloud instances — AWS Graviton, Ampere, etc.

The correct image is selected automatically by Docker based on the host architecture. Both architectures use the same pinned Chrome for Testing build, downloaded from `storage.googleapis.com/chrome-for-testing-public`.

Both variants are functionally identical.

### Quick Start

```shell
# Build locally
docker build . --tag zipreport-server:3.0.0

# Run with API key from the exported variable (uses default config, HTTP only, no TLS)
export ZIPREPORT_API_KEY="$(openssl rand -hex 32)"
docker run -p 6543:6543 -e ZIPREPORT_API_KEY zipreport-server:3.0.0
```

### Production Deployment with Custom Configuration

Create `config/config.json` (see [config.sample.json](./config/config.sample.json) or [complete reference](./docs/configuration.md)):

```json
{
  "apiServer": {
    "host": "",
    "port": 6543,
    "authTokenSecret": "<generate: openssl rand -hex 32>",
    "defaultSecurityHeaders": true,
    "tlsEnable": false
  },
  "zipReport": {
    "concurrency": 8,
    "baseHttpPort": 22000
  },
  "log": {
    "level": "info"
  }
}
```

Then run with your configuration. The mounted directory replaces the image's bundled `/app/config`, so it must contain
`config.json`:

```shell
# Mount your configuration directory
docker run -p 6543:6543 \
  -v $(pwd)/config:/app/config \
  zipreport-server:3.0.0
```

### With TLS/HTTPS

Generate certificates (or use your own):

```shell
cd config
./generate_certs.sh
cd ..
```

The container runs as the unprivileged `zipreport` user, which cannot read a key that is only readable by your host
user (`generate_certs.sh` creates it with mode 0600). Give that user ownership of the key:

```shell
sudo chown "$(docker run --rm --entrypoint id zipreport-server:3.0.0 -u)" config/ssl/server.key
```

Update `config.json` to enable TLS:

```json
{
  "apiServer": {
    "tlsEnable": true,
    "tlsCert": "config/ssl/server.crt",
    "tlsKey": "config/ssl/server.key",
    "authTokenSecret": "<generate: openssl rand -hex 32>"
  }
}
```

Run with TLS:

```shell
docker run -p 6543:6543 \
  -v $(pwd)/config:/app/config \
  zipreport-server:3.0.0
```

Clients then use `https://`; with the self-signed certificate, curl needs `-k` (or `--cacert` with a certificate whose
name matches the host):

```shell
curl -k -X POST https://localhost:6543/v2/render \
  -H "X-Auth-Key: $ZIPREPORT_API_KEY" \
  -F "report=@report.zpt" -F "page_size=A4" -F "margins=standard" \
  -o output.pdf
```

### Using Prebuilt Image

```shell
# Pull the latest release
docker pull ghcr.io/zipreport/zipreport-server:latest

# Or specific version
docker pull ghcr.io/zipreport/zipreport-server:3.0.0

# Run with mounted configuration
docker run -p 6543:6543 \
  -v $(pwd)/config:/app/config \
  ghcr.io/zipreport/zipreport-server:3.0.0
```

### Docker Compose Example

```yaml
services:
  zipreport:
    image: ghcr.io/zipreport/zipreport-server:3.0.0
    ports:
      - "6543:6543"
      # Metrics: the mounted config.json must enable metrics and set prometheus.host to "0.0.0.0"
      - "2220:2220"  # Prometheus metrics (if enabled)
    volumes:
      - ./config:/app/config
    restart: unless-stopped
```

### Configuration Options

See [docs/configuration.md](./docs/configuration.md) for complete configuration reference with 60+ options including:
- API server settings (host, port, timeouts, TLS)
- Authentication configuration
- Prometheus metrics endpoint
- ZipReport rendering engine settings
- Logging configuration with rotation

### Migration from 2.2.x

If upgrading from 2.2.x or earlier, see the [migration guide](./docs/migration-guide.md) for converting command-line arguments to the configuration file format.

## Build

Building from source requires Go 1.27.3 or newer (see `go.mod`).

### Build Binaries

```shell
# Build all binaries (zipreport-server and browser-update)
make build

# Or just build
make
```

Binaries will be created in `./bin/`:
- `bin/zipreport-server` - Main server binary
- `bin/browser-update` - Browser download utility (fetches the pinned Chrome for Testing build for the current platform)

### Updating the Chrome Version

The pinned Chrome for Testing build is the `Version` constant in `pkg/browser/browser.go`. To bump it, set it to the
Stable entry of [`last-known-good-versions.json`](https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions.json),
update the SHA-256 of every platform's zip in the `checksums` map next to it (downloads that do not match are refused,
including the one in the Docker build), update the expected URLs and `chrome-<Version>` directory in
`test/browser_test.go`, and rebuild. The cache directory is named after the version
(`~/.cache/rod/browser/chrome-<Version>`), so a bump never reuses a stale binary. The server always uses this pinned
build; the `bin=` option of rod's `rod` environment variable is ignored.

### Generate Self-Signed Certificates

```shell
# Generate certificates in config/ssl/ (the paths used in the TLS example above)
cd config
./generate_certs.sh

# Or using make; this writes to cert/, so point apiServer.tlsCert/tlsKey at cert/server.crt and cert/server.key
make certificate
```

### Run Tests

```shell
# Tests that don't need Chrome (fast)
make test-short

# Full suite, including browser-based tests (needs a Chrome that can start; same as `make test`)
make test-integration

# Regenerate the .zpt test fixtures from their HTML sources
make test-fixtures
```

Browser-based tests fail on hosts where Chrome cannot start. To run the full suite in a container instead:

```shell
docker build -f Dockerfile.test -t zipreport-test .
docker run --rm -v "$(pwd)/test:/app/test" zipreport-test
```

### Format and Lint

```shell
make fmt

# Requires golangci-lint 2.15.0 (built with Go >= go.mod) and govulncheck 1.9.0; prints install commands if missing
make lint
```

### Run Server Locally

```shell
# Make sure you have a configuration file
cp config/config.sample.json config/config.json

# Set apiServer.authTokenSecret (e.g. openssl rand -hex 32), or export ZIPREPORT_API_KEY;
# the server refuses to start without a compliant secret

# Run the server; the pinned Chrome for Testing build is downloaded on first start if missing
./bin/zipreport-server -c config/config.json

# Or use the default config location (config/config.json)
./bin/zipreport-server

# Print all config options with their defaults
./bin/zipreport-server -sample-config
```

## Sponsors

The [ZipReport Project](https://github.com/zipreport/) is sponsored exclusively by [BlackShield](https://blackshield.pt)


## License

MIT License - see LICENSE file for details

