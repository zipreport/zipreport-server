# zipreport-server

[![Go Version](https://img.shields.io/github/go-mod/go-version/zipreport/zipreport-server)](https://go.dev/)
[![License](https://img.shields.io/github/license/zipreport/zipreport-server)](https://github.com/zipreport/zipreport-server/blob/development/LICENSE)
[![CI](https://github.com/zipreport/zipreport-server/actions/workflows/ci.yml/badge.svg)](https://github.com/zipreport/zipreport-server/actions/workflows/ci.yml)
[![Docker](https://github.com/zipreport/zipreport-server/actions/workflows/docker.yml/badge.svg)](https://github.com/zipreport/zipreport-server/actions/workflows/docker.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/zipreport/zipreport-server)](https://goreportcard.com/report/github.com/zipreport/zipreport-server)

ZipReport-server is the [zipreport](https://github.com/zipreport/zipreport) HTML to PDF conversion daemon, based on
[rod](https://github.com/go-rod/rod) and Chromium, written in Go.

**Note:** zipreport-server 2.xx only works with zipreport library version >= 2.0.0 ,and it is **incompatible** with
previous
versions.

### Upgrading to version 2.3.0

Starting with v2.3.0, zipreport-server uses an external configuration file and no longer bundles a self-signed certificate;
instead, mount a folder with a proper config.json (see [the sample configuration file](./config/config.sample.json)), and
optionally modify and use [the sample script](./config/generate_certs.sh) to generate your own self-signed certificates;
see below for a docker-compose example, and check the [migration guide](./docs/migration-guide.md).

Also, please note that prometheus metrics are now exposed using a dedicated port, and metrics are disabled by default. Check
the [configuration file options](./docs/configuration.md) for available configuration parameters. 

### Security considerations

zipreport-server relies on Chromium to render artifacts into PDF. As such, it allows unfettered execution of any
external dependencies and scripts your template may use. This behavior may pose a security risk on certain
environments.
The daemon also relies on the creation of ephemeral http servers on localhost as part of the rendering process.

### How it works

The zipreport-server API receives a rendering request with an associated ZPT resource. For each rendering request,
zipreport-server
launches an internal http server to serve the ZPT content, and then instructs a Chromium instance to open the temporary
url and render to PDF.

The settling time for the internal HTML/JS rendering process can either be a default value in milisseconds (the default
behavior), or triggered by writing 'zpt-view-ready' to the JS console. By using the console approach, the PDF
generation
is triggered only after all dynamic canvas elements were generated.

### Available endpoints

#### [POST] /v2/render

**Format:** multipart/form-data

**Fields:**

| Field             | Mandatory | Description                                                   |
|-------------------|-----------|---------------------------------------------------------------|
| report            | Yes       | Report file                                                   |
| page_size         | Yes       | Page size (A5/A4/A3/Letter/Legal/Tabloid)                     |
| margins           | Yes       | Margin type (none/minimal/standard)                           |
| landscape         | No        | If true, print in landscape                                   |
| script            | No        | Main html file (default report.html)                          |
| settling_time     | No        | Settling time, in ms (default 200ms, see below)               |
| timeout_job       | No        | Job timeout, in seconds (default 120s, see below)             | 
| timeout_js        | No        | JavaScript event timeout, in seconds (default 30s, see below) |
| js_event          | No        | If true, wait for the javascript event (see below)            |
| ignore_ssl_errors | No        | If true, ssl errors in referenced resources will be ignored   |

**settling_time** (default: 200)

Value in ms to wait after the DOM is ready to print the report. This setting is ignored if
js_event is enabled.

**timeout_job** (default 120)

Waiting time, in seconds, to perform the conversion operation, including waiting times such as
settling_time.

**timeout_js** (default 30)

Time to wait, in seconds, for the javascript event to be triggered, before generating the
report anyway. Requires js_event to be true.

**js_event**

If true, the system will wait upto timeout_js for a specific console message before
generating the PDF. This allows for dynamic pages to signal when DOM manipulation is finished.

Triggering example:

```javascript
<script>
    (function() {
    // signal end of composition
    console.log("zpt-view-ready")
})();
</script>
```


### Optional metrics endpoint (disabled by default)

#### [GET] /metrics

Prometheus metrics endpoint. Besides the default internal Go metrics, the following are provided:

| metric                | type      | description                                                          |
|-----------------------|-----------|----------------------------------------------------------------------|
| total_requests        | counter   | Total conversion requests                                            |
| total_request_success | counter   | Number of successful API calls                                       |
| total_request_error   | counter   | Number of failed API calls                                           |
| conversion_time       | histogram | Elapsed conversion time histogram, in seconds. The upper bound is 120 |
| current_http_servers  | gauge     | Current internal HTTP server count                                   |
| current_browsers      | gauge     | Current internal browser instance count                              |

### Authentication

zipreport-server performs header-based authentication using the token specified in your configuration file (`apiServer.options.authTokenSecret`). Clients must pass the authentication token in the `X-Auth-Key` header.

Example:
```bash
curl -X POST http://localhost:6543/v2/render \
  -H "X-Auth-Key: your-secret-token" \
  -F "report=@report.zpt" \
  -F "page_size=A4" \
  -F "margins=standard" \
  -F "script=index.html" \
  -o output.pdf
```

Without authentication, requests will receive `401 Unauthorized`.

### Running with Docker

Starting with version 2.3.0, zipreport-server uses a configuration file instead of environment variables. You must mount a configuration file to `/app/config/config.json` in the container.

**Quick Start with Default Configuration**

```shell
# Build locally
docker build . --tag zipreport-server:2.3.0

# Run with sample configuration (HTTP only, no TLS)
docker run -p 6543:6543 zipreport-server:2.3.0
```

**Production Deployment with Custom Configuration**

Create a `config.json` file (see [config.sample.json](./config/config.sample.json) or [complete reference](./docs/configuration.md)):

```json
{
  "apiServer": {
    "host": "",
    "port": 6543,
    "options": {
      "authTokenSecret": "your-secure-random-token-here",
      "defaultSecurityHeaders": "1"
    },
    "tlsEnable": false
  },
  "zipReport": {
    "concurrency": 8,
    "baseHttpPort": 42000
  },
  "log": {
    "level": "info"
  }
}
```

Then run with your configuration:

```shell
# Mount your configuration directory
docker run -p 6543:6543 \
  -v $(pwd)/config:/app/config \
  zipreport-server:2.3.0
```

**With TLS/HTTPS**

Generate certificates (or use your own):

```shell
cd config
./generate_certs.sh
```

Update `config.json` to enable TLS:

```json
{
  "apiServer": {
    "tlsEnable": true,
    "tlsCert": "config/ssl/server.crt",
    "tlsKey": "config/ssl/server.key",
    "options": {
      "authTokenSecret": "your-secure-random-token-here"
    }
  }
}
```

Run with TLS:

```shell
docker run -p 6543:6543 \
  -v $(pwd)/config:/app/config \
  zipreport-server:2.3.0
```

**Using Prebuilt Image**

```shell
# Pull latest version
docker pull ghcr.io/zipreport/zipreport-server:latest

# Or specific version
docker pull ghcr.io/zipreport/zipreport-server:2.3.0

# Run with mounted configuration
docker run -p 6543:6543 \
  -v $(pwd)/config:/app/config \
  ghcr.io/zipreport/zipreport-server:2.3.0
```

**Docker Compose Example**

```yaml
version: '3.8'

services:
  zipreport:
    image: ghcr.io/zipreport/zipreport-server:2.3.0
    ports:
      - "6543:6543"
      - "2220:2220"  # Prometheus metrics (if enabled)
    volumes:
      - ./config:/app/config
    restart: unless-stopped
```

**Configuration Options**

See [docs/configuration.md](./docs/configuration.md) for complete configuration reference with 70+ options including:
- API server settings (host, port, timeouts, TLS)
- Authentication configuration
- Prometheus metrics endpoint
- ZipReport rendering engine settings
- Logging configuration with rotation

**Migration from 2.2.x**

If upgrading from 2.2.x or earlier, see the [migration guide](./docs/migration-guide.md) for converting environment variables to the new configuration file format.

### Build

**Build Binaries**

```shell
# Build all binaries (zipreport-server and browser-update)
make build

# Or just build
make
```

Binaries will be created in `./bin/`:
- `bin/zipreport-server` - Main server binary
- `bin/browser-update` - Browser update utility

**Generate Self-Signed Certificates**

```shell
# Generate certificates in config/ssl/
cd config
./generate_certs.sh

# Or using make (generates in cert/ directory)
make certificate
```

**Run Tests**

```shell
# Run all tests
make test

# Run quick tests (skip long-running tests)
make test-short

# Run with extended timeout
make test-integration
```

**Format Code**

```shell
make fmt
```

**Run Server Locally**

```shell
# Make sure you have a configuration file
cp config/config.sample.json config/config.json

# Edit config.json with your settings

# Run the server
./bin/zipreport-server -c config/config.json

# Or use default config location
./bin/zipreport-server
```

