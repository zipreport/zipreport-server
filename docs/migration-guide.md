# Upgrading from 2.4.x to 3.0.0

Version 3.0.0 contains breaking changes. Read every item before upgrading; images tagged `latest` pick them up as soon as
3.0.0 is released. `v2.4.1` remains available for deployments that are not ready to move.

## 1. The API secret is mandatory

The server refuses to start unless `apiServer.authTokenSecret` (or the `ZIPREPORT_API_KEY` environment variable, which
overrides it) is:

- not empty after trimming whitespace;
- not padded with leading or trailing whitespace;
- not one of the placeholder values published in earlier sample configs and docs (for example `my-super-secret-token`,
  `your-secret`, `your-secure-random-token-here`, `<generate: openssl rand -hex 32>`);
- at least 32 characters long.

The shipped sample configs and the Docker image have an empty secret, so a container started without
`ZIPREPORT_API_KEY` (or a mounted config with a compliant secret) exits at startup.

```shell
export ZIPREPORT_API_KEY="$(openssl rand -hex 32)"   # keep it: clients send it as the X-Auth-Key header
docker run -p 6543:6543 -e ZIPREPORT_API_KEY zipreport-server:3.0.0
curl -H "X-Auth-Key: $ZIPREPORT_API_KEY" ...
```

## 2. Rendered pages only reach the network through the egress proxy

All browser traffic of a render job goes through a built-in per-job egress proxy.

- `HTTP_PROXY` / `HTTPS_PROXY` are **ignored** for rendered content. The egress proxy dials directly unless you
  configure `zipReport.upstreamProxy` (available from 3.1.0). If your deployment relied on an outbound
  proxy, external assets (CDN fonts, scripts, images) stop loading until you do.
- A Chrome that runs with a process-wide proxy switch (`--proxy-server`, `--proxy-pac-url`, `--no-proxy-server`) is
  **refused**, and the job fails, because such a browser could carry traffic the egress proxy never sees. Remove the
  switch from any custom launch arguments.
- Loopback, local-interface, private, link-local and cloud-metadata destinations (including host names that resolve to
  them) are blocked. Permit intranet asset hosts with `zipReport.egressAllow`, a list of CIDRs or exact `host:port`
  entries, for example `["10.20.0.0/16", "assets.intranet:443"]`. Always refused, on loopback and local addresses: the
  render-port range, the job's own proxy port, the API port and, if enabled, the metrics port. Allowing loopback ranges
  (e.g. `127.0.0.0/8`) also exposes other local services, including other jobs' egress proxies and Chrome's DevTools
  port; avoid it.

## 3. Listener failures now exit the process

If the API listener or the metrics listener cannot start (for example the port is in use), the process now logs the error
and exits with a non-zero status. Previously the error was logged and the process kept running without the listener.
Make sure your supervisor restarts the process or surfaces the failure.

## 4. Default `baseHttpPort` is 22000

`zipReport.baseHttpPort` now defaults to 22000 (was 42000), below the Linux ephemeral port range (32768-60999), so
outbound sockets can no longer take a report port. A config that sets `baseHttpPort` explicitly keeps its value. At startup
the server logs a warning when the configured range (`baseHttpPort` to `baseHttpPort + concurrency - 1`) overlaps the
ephemeral range; lower `baseHttpPort` or reserve the ports with
`sysctl net.ipv4.ip_local_reserved_ports=<lo>-<hi>`. If you publish or firewall these ports, update the rules.

## 5. API timeouts

| Setting | 3.0.0 |
|---|---|
| `apiServer.writeTimeout` | 720s default; a value of 0 or less falls back to 720 (2.4.x: 60s when omitted, shipped sample 600) |
| `apiServer.readTimeout` | 600s default; a value of 0 or less falls back to 600 |
| header read timeout | 15s, new, not configurable |
| idle keep-alive timeout | 60s, new, not configurable |

- `writeTimeout` covers the upload, so keep it at least `readTimeout`. After the upload is parsed the handler re-arms the
  write deadline to `timeout_job` + `writeTimeout`.
- `timeout_job` now includes the time a job waits for a free render slot.
- A client that disconnects cancels its job; the job stops at its next wait (slot wait, page creation, JS-event or
  settling wait) and its result reports `ErrJobCancelled`; navigation and PDF steps report the underlying context error.
- Known limits: a client that half-closes its socket after sending the request cancels its own job, and disconnect
  detection only starts once the request body has been read to EOF.
- If your config sets `writeTimeout: 600`, it still works (it only needs to cover the upload, at least `readTimeout`); the default is 720.

## 6. Chrome for Testing 156 with site isolation

The bundled browser moves from Chromium ~131 to Chrome for Testing 156.0.8078.47 on all architectures, with site isolation
enabled.

- Expect higher memory use per job (more renderer processes); re-check container memory limits and `concurrency`.
- Cross-site iframes run out of process. A `console.log("zpt-view-ready")` emitted from a cross-site iframe is no longer
  seen by js_event mode and the job hits the JS timeout. Signal readiness from the top-level page.
- The `bin=` option of the `rod` environment variable is ignored; the browser is always the pinned build.

## 7. `enableHttpDebugging` now works

`zipReport.enableHttpDebugging` was a no-op. Configs that already set it to `true` start emitting one Info log line per
report-server request (capped per job). It needs `log.level` of `info` or lower; a startup warning is logged otherwise.

## 8. `zipReport` read and write timeouts now apply

`zipReport.readTimeoutSeconds` and `zipReport.writeTimeoutSeconds` (default 300 each) were ignored. They now set the read
and write timeouts of the per-job report servers that serve the ZIP contents to the browser. Slow assets that took longer
than these limits previously succeeded and may now fail; raise the values if needed.

## 9. Go 1.27.3 toolchain

Building from source requires Go 1.27.3 (`go.mod`); the Docker builder image is `golang:1.27.3`.

## Other changes worth knowing

- Render jobs run in isolated, disposable browser contexts, `ignore_ssl_errors` is scoped to the job's page, and the
  browser HTTP cache no longer persists between jobs.
- Graceful stop drains in-flight renders for up to 8 seconds.
- Every log line includes a `hostname` field (`log.includeHostname`, default `true`).
- Podman and Kubernetes containers are detected like Docker (`--no-sandbox`, shared browser).

See [CHANGELOG.md](../CHANGELOG.md) for the full list.

---

# Migration Guide: 2.2.x to 2.3.0

This guide helps you migrate from ZipReport Server 2.2.x to 2.3.0 release with enhanced security and testing capabilities.

## Overview of Changes

The new version includes significant security improvements, comprehensive testing, and better configuration 
management. Most changes are backward-compatible, but some require attention.

## Breaking Changes

### Configuration System Change: Command-Line Args → Configuration File

**IMPORTANT: This is a major breaking change from version 2.1.x and earlier.**

#### What Changed

Version 2.3.0 introduced a complete configuration system overhaul. The server now uses a JSON configuration file instead of command-line arguments.

#### Before (2.1.x and earlier) - Command-Line Arguments

```bash
./zipreport-server \
  -addr 127.0.0.1 \
  -port 6543 \
  -apikey "your-secret-key" \
  -certkey "/path/to/key.pem" \
  -certificate "/path/to/cert.pem" \
  -httprt 600 \
  -httpwt 600 \
  -debug false \
  -console false \
  -nometrics false \
  -concurrency 8 \
  -baseport 22000 \
  -loglevel 1
```

#### After (2.2.0+) - Configuration File

```json
{
  "apiServer": {
    "host": "127.0.0.1",
    "port": 6543,
    "readTimeout": 600,
    "writeTimeout": 720,
    "debug": false,
    "tlsEnable": true,
    "tlsCert": "/path/to/cert.pem",
    "tlsKey": "/path/to/key.pem",
    "authTokenHeader": "X-Auth-Key",
    "authTokenSecret": "<generate: openssl rand -hex 32>",
    "defaultSecurityHeaders": true
  },
  "zipReport": {
    "enableConsoleLogging": false,
    "enableMetrics": true,
    "concurrency": 8,
    "baseHttpPort": 22000
  },
  "log": {
    "level": "info"
  }
}
```

### Command-Line Argument Mapping

Here's how the old command-line arguments map to the new configuration file:

| Old Flag         | New Configuration Path                  | Notes                                      |
|------------------|-----------------------------------------|--------------------------------------------|
| `-addr`          | `apiServer.host`                        |                                            |
| `-port`          | `apiServer.port`                        |                                            |
| `-apikey`        | `apiServer.authTokenSecret`             |                                            |
| `-certkey`       | `apiServer.tlsKey`                      |                                            |
| `-certificate`   | `apiServer.tlsCert`                     |                                            |
| `-httprt`        | `apiServer.readTimeout`                 |                                            |
| `-httpwt`        | `apiServer.writeTimeout`                |                                            |
| `-debug`         | `apiServer.debug`                       |                                            |
| `-console`       | `zipReport.enableConsoleLogging`        |                                            |
| `-nometrics`     | `zipReport.enableMetrics`               | Inverted logic: nometrics → enableMetrics  |
| `-concurrency`   | `zipReport.concurrency`                 |                                            |
| `-baseport`      | `zipReport.baseHttpPort`                |                                            |
| `-loglevel`      | `log.level`                             | Now uses string: "debug", "info", "error" |
| (new)            | `apiServer.authTokenHeader`             | "X-Auth-Key"                               |
| (new)            | `apiServer.defaultSecurityHeaders`      | boolean, `true` to enable                  |

### Migration Steps

#### Step 1: Create Configuration File

Create a new file `config/config.json` based on your current command-line arguments:

```bash
# If you were running:
./zipreport-server -addr 0.0.0.0 -port 6543 -apikey "my-secret" -concurrency 4

# Create config/config.json:
{
  "apiServer": {
    "host": "0.0.0.0",
    "port": 6543,
    "authTokenHeader": "X-Auth-Key",
    "authTokenSecret": "<generate: openssl rand -hex 32>",
    "defaultSecurityHeaders": true
  },
  "zipReport": {
    "concurrency": 4
  }
}
```

#### Step 2: Generate Sample Configuration

Use the built-in sample config generator:

```bash
./zipreport-server -sample-config > config/config.json
```

Then edit the file with your specific values. The generated file has an empty `apiServer.authTokenSecret` and the server will not start until you set it (or `ZIPREPORT_API_KEY`) to a secret of at least 32 characters, e.g. the output of `openssl rand -hex 32`.

#### Step 3: Update Startup Scripts

**Old startup:**
```bash
#!/bin/bash
./zipreport-server -addr 0.0.0.0 -port 6543 -apikey "$API_KEY" -concurrency 8
```

**New startup:**
```bash
#!/bin/bash
./zipreport-server -c config/config.json
```

Or use the default location:
```bash
#!/bin/bash
./zipreport-server
# Automatically loads config/config.json
```

#### Step 4: Update Systemd Service (if applicable)

**Old service file:**
```ini
[Service]
ExecStart=/usr/local/bin/zipreport-server -addr 0.0.0.0 -port 6543 -apikey "secret"
```

**New service file:**
```ini
[Service]
WorkingDirectory=/opt/zipreport
ExecStart=/usr/local/bin/zipreport-server -c /opt/zipreport/config/config.json
```

### Removed Command-Line Flags

The following flags no longer exist:
- `-addr` → use config file
- `-port` → use config file
- `-apikey` → use config file
- `-certkey` → use config file
- `-certificate` → use config file
- `-httprt` → use config file
- `-httpwt` → use config file
- `-debug` → use config file
- `-console` → use config file
- `-nometrics` → use config file
- `-concurrency` → use config file
- `-baseport` → use config file
- `-loglevel` → use config file

### Remaining Command-Line Flags

Only these flags remain:
- `-c <file>` - Specify configuration file path (default: `config/config.json`)
- `-version` - Show version
- `-sample-config` - Generate sample configuration

### API Compatibility

All existing API endpoints remain backward compatible. No changes required to client applications.

## Security Enhancements

### 1. Path Traversal Protection

**What Changed:**
- Enhanced HTTP file server path validation
- Improved sanitization of file paths in ZIP archives
- Additional checks for traversal attempts using backslashes

**Impact:** None on normal usage. Malicious path traversal attempts will now be blocked more effectively.

**Action Required:** None. Protection is automatic.

### 2. Input Validation

**What Changed:**
- Negative margin values are now rejected
- Stricter validation on margin parameters

**Impact:** Requests with negative margins will receive `400 Bad Request` instead of potentially causing rendering issues.

**Action Required:**
- If you're sending negative margin values (which was never intended), update your client code to send valid values (>= 0)

**Example:**
```bash
# Before (may have caused issues)
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: $ZIPREPORT_API_KEY" \
  -F "report=@report.zpt" \
  -F "margins=custom" \
  -F "margin_left=-1.0"  # ❌ Now rejected

# After (correct usage)
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: $ZIPREPORT_API_KEY" \
  -F "report=@report.zpt" \
  -F "margins=custom" \
  -F "margin_left=0.5"   # ✅ Valid
```

## New Features

### 1. Comprehensive Test Suite

**What's New:**
- 17+ integration tests covering all API endpoints
- Security tests for path traversal and input validation
- Test fixtures for development

**How to Use:**
```bash
# Run all tests
make test

# Run quick tests (skip long-running tests)
make test-short

# Run full integration tests with timeout
make test-integration
```

### 2. Configuration Documentation

**What's New:**
- Complete configuration reference in `docs/configuration.md`
- All 60+ configuration options documented
- Examples for common scenarios

**How to Use:**
- Review `docs/configuration.md` for configuration options
- Use `config/config.complete.json` as a reference
- Run `./zipreport-server -sample-config` to see defaults

### 3. Code Formatting Target

**What's New:**
- `make fmt` target for code formatting

**How to Use:**
```bash
make fmt
```

## Configuration Changes

### Configuration File Changes

Configuration changes since 2.4.x are listed in "Upgrading from 2.4.x to 3.0.0" at the top of this guide. In particular a
compliant `authTokenSecret` (or `ZIPREPORT_API_KEY`) is now required.

### Recommended Configuration Review

1. **Authentication Token**
   ```json
   {
     "apiServer": {
       "authTokenSecret": "<generate: openssl rand -hex 32>"
     }
   }
   ```

   **Recommendation:** Ensure you're using a strong, randomly generated token in production.

2. **SSL/TLS Configuration**

   Review the enhanced TLS documentation in `docs/configuration.md` for production deployments.

3. **Timeout Values**

   While there are no upper bounds enforced yet, keep timeout values reasonable:
   ```json
   {
     "apiServer": {
       "readTimeout": 600,
       "writeTimeout": 720
     },
     "zipReport": {
       "readTimeoutSeconds": 300,
       "writeTimeoutSeconds": 300
     }
   }
   ```

## API Changes

### No Breaking API Changes

All existing API endpoints work exactly as before:
- `POST /v2/render` - No changes to request/response format

### Enhanced Error Responses

**What Changed:**
More consistent error messages for validation failures.

**Before:**
```json
{"error": "error building render job"}
```

**After (more specific):**
```json
{"error": "error building render job"}
```

Error messages in logs now include more context for debugging.

## Deployment Guide

### Step 1: Backup

```bash
# Backup your current configuration
cp config/config.json config/config.json.backup

# Backup your binary
cp bin/zipreport-server bin/zipreport-server.backup
```

### Step 2: Update Dependencies

```bash
# Pull latest code
git pull origin development

# Update Go modules
go mod tidy
```

### Step 3: Build

```bash
# Build the new version
make build

# Or just
make
```

### Step 4: Test (Recommended)

```bash
# Run the test suite to verify everything works
make test-short

# Check version
./bin/zipreport-server -version
```

### Step 5: Deploy

#### Option A: In-Place Upgrade (with downtime)

```bash
# Stop the current server
sudo systemctl stop zipreport-server

# Deploy new binary
sudo cp bin/zipreport-server /usr/local/bin/

# Start the new server
sudo systemctl start zipreport-server

# Check status
sudo systemctl status zipreport-server
```

#### Option B: Rolling Upgrade (no downtime)

If running multiple instances behind a load balancer:

```bash
# For each instance:
1. Remove from load balancer
2. Stop instance
3. Deploy new binary
4. Start instance
5. Health check
6. Add back to load balancer
```

### Step 6: Verify

```bash
# Check server is running
curl -I http://localhost:6543/

# Test render endpoint with the included test file
# First, ensure you have a test report file. You can use the one from test fixtures:
cp test/fixtures/test.zpt /tmp/

# Now test rendering
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: $ZIPREPORT_API_KEY" \
  -F "report=@/tmp/test.zpt" \
  -F "page_size=A4" \
  -F "margins=standard" \
  -F "script=test.html" \
  -o /tmp/output.pdf

# Verify PDF was created successfully
file /tmp/output.pdf
# Expected output: /tmp/output.pdf: PDF document, version 1.4

# Check PDF is valid (should show page count)
pdfinfo /tmp/output.pdf 2>/dev/null | grep Pages || echo "PDF created successfully"
```

## Rollback Procedure

If you encounter issues:

```bash
# Stop the new version
sudo systemctl stop zipreport-server

# Restore previous binary
sudo cp bin/zipreport-server.backup /usr/local/bin/zipreport-server

# Restore configuration (if changed)
cp config/config.json.backup config/config.json

# Start the old version
sudo systemctl start zipreport-server
```

## Testing Your Upgrade

### 1. Basic Health Check

```bash
# Server responds
curl -I http://localhost:6543/
```

Expected: HTTP 200 or 404 (both indicate server is running)

### 2. Authentication Check

```bash
# Without token (should fail)
curl -X POST http://localhost:6543/v2/render \
  -F "report=@test.zpt"
```

Expected: HTTP 401 Unauthorized

### 3. Render Test

```bash
# Valid request
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: $ZIPREPORT_API_KEY" \
  -F "report=@test.zpt" \
  -F "page_size=A4" \
  -F "margins=standard" \
  -F "script=index.html" \
  -o test-output.pdf

# Verify PDF
file test-output.pdf
```

Expected: Valid PDF file created

### 4. Validation Test

```bash
# Test negative margin rejection
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: $ZIPREPORT_API_KEY" \
  -F "report=@test.zpt" \
  -F "margins=custom" \
  -F "margin_left=-1.0"
```

Expected: HTTP 400 Bad Request
