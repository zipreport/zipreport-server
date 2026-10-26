# Configuration Reference

This document describes all available configuration options for ZipReport Server.

## Configuration File

The configuration file is a JSON file that defines settings for the API server, Prometheus metrics, ZipReport rendering
engine, and logging. A complete example is available at `config/config.complete.json`.

## Environment Variable Overrides

The following environment variables can be used to override configuration file values. This is particularly useful for Docker deployments and CI/CD pipelines.

| Variable           | Overrides                          | Description                                    |
|--------------------|------------------------------------|------------------------------------------------|
| `ZIPREPORT_API_KEY`| `apiServer.authTokenSecret`| API authentication key. Takes precedence over config file. |
| `ZIPREPORT_UPSTREAM_PROXY`| `zipReport.upstreamProxy.url`| Upstream proxy URL, so a proxy password can stay out of the config file. `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are ignored. |

Example:
```shell
export ZIPREPORT_API_KEY="$(openssl rand -hex 32)"   # keep it: clients send it as the X-Auth-Key header
docker run -p 6543:6543 -e ZIPREPORT_API_KEY zipreport-server:latest
```

## Configuration Sections

### apiServer

Configuration for the HTTP API server that handles rendering requests.

| Field                            | Type    | Default                   | Description                                                                                    |
|----------------------------------|---------|---------------------------|------------------------------------------------------------------------------------------------|
| `host`                           | string  | `""`                      | Hostname or IP address to bind the server to. Empty string binds to all interfaces.            |
| `port`                           | integer | `6543`                    | Port number for the API server to listen on. Must not fall inside the `baseHttpPort` range or equal `prometheus.port` (when metrics are enabled); `0` is treated as `5000`. |
| `readTimeout`                    | integer | `600`                     | Maximum duration in seconds for reading the entire request, including the body.                |
| `writeTimeout`                   | integer | `720`                     | Write deadline in seconds. It covers the upload (so keep it at least `readTimeout`); once the upload is parsed the handler re-arms it to `timeout_job` + `writeTimeout`. |
| `debug`                          | boolean | `false`                   | Enable debug mode for additional logging and diagnostic information.                           |
| `serverName`                     | string  | `"http"`                  | Name of the HTTP request logger (module field in request log lines).                           |
| `authTokenHeader`                | string  | `"X-Auth-Key"`            | HTTP header that carries the authentication token.                                             |
| `authTokenSecret`                | string  | `""` | Required, at least 32 characters, no leading/trailing whitespace. The server refuses to start if it is empty, too short, or a placeholder from these docs. `ZIPREPORT_API_KEY` overrides it. Generate with `openssl rand -hex 32`. |
| `defaultSecurityHeaders`         | boolean | `true`                    | Enable default security headers in HTTP responses.                                             |
| `trustedProxies`                 | array   | `[]`                      | Trusted proxy IP addresses or CIDR ranges. With the default `[]` no proxy is trusted: `X-Forwarded-For` and `X-Real-IP` are ignored and the logged `client_ip` is the socket peer. Set it when running behind a reverse proxy whose client IPs you want logged. |
| `tlsEnable`                      | boolean | `false`                   | Enable TLS/HTTPS for the API server.                                                           |
| `tlsCert`                        | string  | `""`                      | Path to TLS certificate file (PEM format).                                                     |
| `tlsKey`                         | string  | `""`                      | Path to TLS private key file (PEM format).                                                     |
| `tlsKeyPassword`                 | string  | `""`                      | Password for encrypted TLS private key.                                                        |
| `tlsKeyPasswordEnvVar`           | string  | `""`                      | Environment variable name containing the TLS key password.                                     |
| `tlsKeyPasswordFile`             | string  | `""`                      | Path to file containing the TLS key password.                                                  |
| `tlsAllowedCACerts`              | array   | `null`                    | List of paths to CA certificate files for client certificate validation.                       |
| `tlsCipherSuites`                | array   | `null`                    | List of allowed TLS cipher suites. If null, uses Go's default secure cipher suites.            |
| `tlsMinVersion`                  | string  | `""`                      | Minimum TLS version (e.g., "1.2", "1.3"). Empty string uses Go's default.                      |
| `tlsMaxVersion`                  | string  | `""`                      | Maximum TLS version (e.g., "1.2", "1.3"). Empty string uses Go's default.                      |
| `tlsAllowedDNSNames`             | array   | `null`                    | List of allowed DNS names for client certificate validation.                                   |

`0` or negative `readTimeout`/`writeTimeout` values mean "use the default". Header read is limited to 15s and idle keep-alive to 60s. Time spent waiting for a free render slot counts against `timeout_job`, and a client that disconnects cancels its job.

### prometheus

Configuration for the Prometheus metrics endpoint.

| Field                  | Type    | Default       | Description                                                                         |
|------------------------|---------|---------------|-------------------------------------------------------------------------------------|
| `host`                 | string  | `"localhost"` | Address to bind the metrics server to. Keep `localhost` on bare metal. In containers set `0.0.0.0` (see below). |
| `port`                 | integer | `2220`        | Port number for the Prometheus metrics endpoint (1024-65535).                       |
| `endpoint`             | string  | `"/metrics"`  | HTTP path for the metrics endpoint. Must be a valid Go `ServeMux` pattern (normally starting with `/`); an empty or invalid value is refused at startup. |
| `tlsEnable`            | boolean | `false`       | Enable TLS/HTTPS for the metrics endpoint.                                          |
| `tlsCert`              | string  | `""`          | Path to TLS certificate file (PEM format).                                          |
| `tlsKey`               | string  | `""`          | Path to TLS private key file (PEM format).                                          |
| `tlsKeyPassword`       | string  | `""`          | Password for encrypted TLS private key.                                             |
| `tlsKeyPasswordEnvVar` | string  | `""`          | Environment variable name containing the TLS key password.                          |
| `tlsKeyPasswordFile`   | string  | `""`          | Path to file containing the TLS key password.                                       |
| `tlsAllowedCACerts`    | array   | `null`        | List of paths to CA certificate files for client certificate validation.            |
| `tlsCipherSuites`      | array   | `null`        | List of allowed TLS cipher suites. If null, uses Go's default secure cipher suites. |
| `tlsMinVersion`        | string  | `""`          | Minimum TLS version (e.g., "1.2", "1.3"). Empty string uses Go's default.           |
| `tlsMaxVersion`        | string  | `""`          | Maximum TLS version (e.g., "1.2", "1.3"). Empty string uses Go's default.           |
| `tlsAllowedDNSNames`   | array   | `null`        | List of allowed DNS names for client certificate validation.                        |

**Metrics in containers**

- Inside a container, `localhost` is the container's own loopback and is unreachable from outside, so a mounted config that enables metrics must set `"host": "0.0.0.0"`.
- The endpoint is unauthenticated and includes Go runtime and process metrics (`go_*`, `process_*`) in addition to the zipreport metrics.
- With `0.0.0.0` the port is reachable from other containers on the same Docker bridge or user-defined network and from other pods in Kubernetes (via the pod IP), even without `-p`. Under `--network host` it is exposed on every host interface. Restrict access with network policy or firewalling, or enable `tlsEnable` and scrape over a private network.

### zipReport

Configuration for the ZipReport rendering engine.

| Field                  | Type    | Default | Description                                                                                |
|------------------------|---------|---------|--------------------------------------------------------------------------------------------|
| `readTimeoutSeconds`   | integer | `300`   | Read timeout, in seconds, of the ephemeral per-job HTTP server that serves report files.   |
| `writeTimeoutSeconds`  | integer | `300`   | Write timeout, in seconds, of the ephemeral per-job HTTP server that serves report files.  |
| `enableConsoleLogging` | boolean | `false` | Log browser console output (`console.*` calls and Chrome's own log entries, such as failed resource loads) at Info level, capped at 200 entries per job with one `console output truncated` warning. String, number and boolean arguments are logged as their value, objects as Chrome's short description (e.g. `Object`, `Array(3)`). |
| `enableHttpDebugging`  | boolean | `false` | Log one line per request reaching the per-job ephemeral servers (method, path, status, bytes, duration; no query strings or bodies) at Info level, capped at 1000 lines per job with a final count of suppressed lines. Requests rejected by Go's HTTP server before the handler (malformed or oversized headers) are not logged. Opt-in: report JavaScript controls how many requests a job makes. |
| `enableMetrics`        | boolean | `false` | Enable metrics collection for rendering operations.                                        |
| `concurrency`          | integer | `8`     | Number of concurrent browser instances for parallel rendering.                             |
| `baseHttpPort`         | integer | `22000` | Base port number for browser instances. Each instance uses baseHttpPort + instance number. Keep baseHttpPort through baseHttpPort+concurrency-1 below the lower bound of `/proc/sys/net/ipv4/ip_local_port_range` (32768 on Linux), or reserve the range with `net.ipv4.ip_local_reserved_ports`; outbound sockets can otherwise take a report port. A warning is logged at startup if they overlap. Must be at least 1024, and `baseHttpPort + concurrency - 1` must not exceed 65535; `apiServer.port` and (when metrics are enabled) `prometheus.port` must lie outside the range, or startup is refused. |
| `egressAllow`          | array of strings | `[]` | CIDRs or exact `host:port` entries a rendered page may reach in addition to public addresses, e.g. `["10.20.0.0/16", "assets.intranet:443"]`. A bare IP needs `/32` or `/128`. Entries can never open, on loopback and local addresses, other jobs' render ports (the job's own server is reached through its `localhost:PORT` name), the job's own proxy port, the API port or, if enabled, the metrics port. Allowing loopback ranges still exposes other local services, including other jobs' egress proxies and Chrome's DevTools port; an entry covering Kubernetes Service/pod or Docker bridge CIDRs can reach this or other replicas' API and metrics ports through non-local addresses, which the hard-deny list does not cover. |
| `upstreamProxy.url`    | string  | `""`    | `http://[user:pass@]host:port` or `https://...` of a corporate proxy the per-job egress proxy chains public traffic through. Empty disables chaining. Basic proxy auth only, from the URL userinfo; `ZIPREPORT_UPSTREAM_PROXY` overrides it so the password can stay out of the file. Only **public** destinations use the upstream: anything reachable only because of `egressAllow` (loopback, private, local-interface addresses) is always dialled directly. The upstream proxy itself (all of its addresses, all ports) is never reachable from a rendered page. A `407` blocks all upstream use for 5 minutes (so a wrong password costs one failed login per cool-down, not one per asset); upstream failures are counted and logged per job, and an upstream error page can be relayed to the page and so appear in the PDF. `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` stay ignored. |
| `upstreamProxy.resolve`| string  | `"local"` | `"local"`: the egress proxy resolves and vets every destination itself, then asks the upstream for a tunnel to the **vetted IP** (`CONNECT 93.184.216.34:443`; plain `http://` assets travel in a `CONNECT <ip>:80` tunnel), so every egress guarantee still holds. The cost: **Squid's default `http_access deny CONNECT !SSL_ports` blocks port 80 and every non-443 https port** (allow them for plain-http or non-443 assets), and proxies that allow-list by host name or refuse IP destinations reject this mode entirely. `"upstream"`: hand the upstream the **host name**, for proxies that filter by name and networks where only the proxy can resolve internet names. **This moves the security boundary to the upstream proxy**: any name the local resolver cannot resolve is handed over unchecked, so a page can reach anything the upstream can reach (including the upstream host's own loopback services). **Destination ACLs on the upstream are required** (Squid: `http_access deny to_localhost`, `to_linklocal` and the private ranges). **Never use it with an upstream that shares this server's network namespace** (same host, or a sidecar in the same pod, even addressed through a Service IP): the upstream's loopback is then this server's loopback, Chrome's DevTools port becomes reachable and a page could take over the browser. The server refuses the mode only when it can detect a same-host upstream (it resolves to a loopback or local-interface address); a Service IP cannot be detected. An exact `egressAllow` `host:port` entry needs a locally resolvable name in this mode. |
| `upstreamProxy.noProxy`| array of strings | `[]` | Public CIDRs or exact `host:port` entries dialled directly instead of through the upstream (still vetted). Private CIDRs are rejected at startup: private destinations are always dialled directly anyway. |

### log

Configuration for application logging.

| Field              | Type    | Default                                 | Description                                                         |
|--------------------|---------|-----------------------------------------|---------------------------------------------------------------------|
| `level`            | string  | `"info"`                                | Logging level. Options: "debug", "info", "warn", "error", "fatal".  |
| `format`           | string  | `"pretty"`                              | Console output format. Options: "pretty" (human-readable), "json".  |
| `includeTimestamp` | boolean | `true`                                  | Include timestamp in log entries.                                   |
| `includeCaller`    | boolean | `false`                                 | Include caller information (file and line number) in log entries.   |
| `includeHostname`  | boolean | `true`                                  | Include hostname in log entries.                                    |
| `callerSkipFrames` | integer | `3`                                     | Number of stack frames to skip when determining caller information. |
| `timeFormat`       | string  | `"2006-01-02T15:04:05.999999999Z07:00"` | Go time format string for timestamps (RFC3339 with nanoseconds).    |
| `noColor`          | boolean | `false`                                 | Disable colored output in console logs.                             |
| `outputToFile`     | boolean | `false`                                 | Enable logging to a file in addition to console.                    |
| `filePath`         | string  | `"application.log"`                     | Path to the log file when file logging is enabled.                  |
| `fileAppend`       | boolean | `true`                                  | Append to existing log file instead of overwriting.                 |
| `filePermissions`  | integer | `420`                                   | Unix file permissions for log files (420 = 0644 in octal).          |
| `fileFormat`       | string  | `"json"`                                | File output format. Options: "json", "pretty".                      |
| `fileRotation`     | boolean | `false`                                 | Enable automatic log file rotation.                                 |
| `maxSizeMb`        | integer | `100`                                   | Maximum size in megabytes before rotating the log file.             |
| `maxBackups`       | integer | `5`                                     | Maximum number of old log files to retain.                          |
| `maxAgeDays`       | integer | `30`                                    | Maximum number of days to retain old log files.                     |
| `compress`         | boolean | `true`                                  | Compress rotated log files using gzip.                              |

## Configuration Examples

### Minimal Production Configuration

```json
{
  "apiServer": {
    "host": "0.0.0.0",
    "port": 6543,
    "authTokenSecret": "<generate: openssl rand -hex 32>"
  },
  "zipReport": {
    "concurrency": 4
  },
  "log": {
    "level": "info",
    "format": "json"
  }
}
```

### HTTPS-Enabled Configuration

```json
{
  "apiServer": {
    "host": "0.0.0.0",
    "port": 6543,
    "tlsEnable": true,
    "tlsCert": "/path/to/cert.pem",
    "tlsKey": "/path/to/key.pem",
    "tlsMinVersion": "1.2",
    "authTokenSecret": "<generate: openssl rand -hex 32>"
  }
}
```

### High-Concurrency Configuration

The secret is not part of this example: supply `apiServer.authTokenSecret` (at least 32 characters, e.g. `openssl rand -hex 32`) in the file or through `ZIPREPORT_API_KEY`.

```json
{
  "apiServer": {
    "port": 6543,
    "readTimeout": 900,
    "writeTimeout": 900
  },
  "zipReport": {
    "readTimeoutSeconds": 600,
    "writeTimeoutSeconds": 600,
    "concurrency": 16,
    "baseHttpPort": 22000
  }
}
```

### Development Configuration with Debug Logging

The secret is not part of this example: supply `apiServer.authTokenSecret` (at least 32 characters, e.g. `openssl rand -hex 32`) in the file or through `ZIPREPORT_API_KEY`.

```json
{
  "apiServer": {
    "port": 6543,
    "debug": true
  },
  "zipReport": {
    "enableConsoleLogging": true,
    "enableHttpDebugging": true,
    "enableMetrics": true,
    "concurrency": 2
  },
  "log": {
    "level": "debug",
    "format": "pretty",
    "includeCaller": true
  }
}
```

## Security Considerations

1. **Authentication Secret**: Supply a unique `apiServer.authTokenSecret` (or `ZIPREPORT_API_KEY`); there is no default
   and the server refuses to start without one. Use a strong, randomly generated secret.

2. **TLS Certificates**: When enabling TLS, ensure certificates are properly secured with appropriate file permissions (
   e.g., 0600 for private keys).

3. **TLS Key Passwords**: Prefer using `tlsKeyPasswordFile` over `tlsKeyPassword` to avoid
   storing passwords in the configuration file. `tlsKeyPasswordEnvVar` is read but no longer cleared from the
   environment, and the Chromium processes this server launches inherit the environment, so the password is visible
   to them.

4. **Trusted Proxies**: Only add verified proxy IP addresses to `trustedProxies` to prevent IP spoofing attacks.

5. **File Permissions**: Log files will be created with permissions specified in `log.filePermissions`. The default (
   420 = 0644) makes files readable by all users but writable only by the owner.

6. **Browser**: The browser is always the pinned Chrome for Testing build (`browser.Version`); the `bin=` option of rod's
   `rod` environment variable is no longer honoured. Chrome runs with site isolation enabled, which starts more renderer
   processes per job, so container memory limits may need raising. Site isolation only contains a renderer exploit when
   Chrome's sandbox is on; in containers Chrome runs with `--no-sandbox`.

## Environment Variables

Configuration values can be overridden using environment variables. The TLS key password supports
`tlsKeyPasswordEnvVar`, but prefer `tlsKeyPasswordFile`: the variable is not cleared from the environment after it is
read, so Chromium child processes inherit it.

## Loading Configuration

The server reads a single configuration file, given with `-c <path>` (default `config/config.json`, relative to the
working directory). If the file cannot be read, startup fails.

- `zipreport-server -sample-config` prints the complete default configuration as JSON; use
  `zipreport-server -sample-config > config.json` to start a new file.
- `zipreport-server -version` prints the version.
- `ZIPREPORT_API_KEY` overrides `apiServer.authTokenSecret` (see [Environment Variables](#environment-variables)).

A sample configuration file is available at `config/config.sample.json`.
