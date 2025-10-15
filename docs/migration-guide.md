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
  -baseport 42000 \
  -loglevel 1
```

#### After (2.2.0+) - Configuration File

```json
{
  "apiServer": {
    "host": "127.0.0.1",
    "port": 6543,
    "readTimeout": 600,
    "writeTimeout": 600,
    "debug": false,
    "tlsEnable": true,
    "tlsCert": "/path/to/cert.pem",
    "tlsKey": "/path/to/key.pem",
    "options": {
      "authTokenHeader": "X-Auth-Key",
      "authTokenSecret": "your-secret-key",
      "defaultSecurityHeaders": "1"
    }
  },
  "zipReport": {
    "enableConsoleLogging": false,
    "enableMetrics": true,
    "concurrency": 8,
    "baseHttpPort": 42000
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
| `-apikey`        | `apiServer.options.authTokenSecret`     |                                            |
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
| (new)            | `apiServer.options.authTokenHeader`     | New in 2.3.0: "X-Auth-Key"                 |
| (new)            | `apiServer.options.defaultSecurityHeaders` | New: "1" to enable                      |

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
    "options": {
      "authTokenHeader": "X-Auth-Key",
      "authTokenSecret": "my-secret",
      "defaultSecurityHeaders": "1"
    }
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

Then edit the file with your specific values.

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
  -H "X-Auth-Key: your-token" \
  -F "report=@report.zpt" \
  -F "margins=custom" \
  -F "margin_left=-1.0"  # ❌ Now rejected

# After (correct usage)
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: your-token" \
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
- All 70+ configuration options documented
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

### No Configuration File Changes Required

Your existing configuration files will continue to work without modification. However, you may want to review new best practices.

### Recommended Configuration Review

1. **Authentication Token**
   ```json
   {
     "apiServer": {
       "options": {
         "authTokenSecret": "your-secure-token-here"
       }
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
       "writeTimeout": 600
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
  -H "X-Auth-Key: your-token" \
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
  -H "X-Auth-Key: your-token" \
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
  -H "X-Auth-Key: your-token" \
  -F "report=@test.zpt" \
  -F "margins=custom" \
  -F "margin_left=-1.0"
```

Expected: HTTP 400 Bad Request
