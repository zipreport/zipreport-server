# Configuration Reference

This document describes all available configuration options for ZipReport Server.

## Configuration File

The configuration file is a JSON file that defines settings for the API server, Prometheus metrics, ZipReport rendering
engine, and logging. A complete example is available at `config/config.complete.json`.

## Configuration Sections

### apiServer

Configuration for the HTTP API server that handles rendering requests.

| Field                            | Type    | Default                   | Description                                                                                    |
|----------------------------------|---------|---------------------------|------------------------------------------------------------------------------------------------|
| `host`                           | string  | `""`                      | Hostname or IP address to bind the server to. Empty string binds to all interfaces.            |
| `port`                           | integer | `6543`                    | Port number for the API server to listen on.                                                   |
| `readTimeout`                    | integer | `600`                     | Maximum duration in seconds for reading the entire request, including the body.                |
| `writeTimeout`                   | integer | `600`                     | Maximum duration in seconds for writing the response.                                          |
| `debug`                          | boolean | `false`                   | Enable debug mode for additional logging and diagnostic information.                           |
| `options.authTokenSecret`        | string  | `"my-super-secret-token"` | Secret key used for authentication token generation and validation. Change this in production. |
| `options.defaultSecurityHeaders` | string  | `"1"`                     | Enable default security headers in HTTP responses. Set to "1" to enable.                       |
| `trustedProxies`                 | array   | `[]`                      | List of trusted proxy IP addresses or CIDR ranges for X-Forwarded-For header processing.       |
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

### prometheus

Configuration for the Prometheus metrics endpoint.

| Field                  | Type    | Default       | Description                                                                         |
|------------------------|---------|---------------|-------------------------------------------------------------------------------------|
| `host`                 | string  | `"localhost"` | Hostname or IP address to bind the metrics server to.                               |
| `port`                 | integer | `2220`        | Port number for the Prometheus metrics endpoint.                                    |
| `endpoint`             | string  | `"/metrics"`  | HTTP path for the metrics endpoint.                                                 |
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

### zipReport

Configuration for the ZipReport rendering engine.

| Field                  | Type    | Default | Description                                                                                |
|------------------------|---------|---------|--------------------------------------------------------------------------------------------|
| `readTimeoutSeconds`   | integer | `300`   | Maximum duration in seconds for reading requests to the rendering engine.                  |
| `writeTimeoutSeconds`  | integer | `300`   | Maximum duration in seconds for writing responses from the rendering engine.               |
| `enableConsoleLogging` | boolean | `false` | Enable logging of browser console output during rendering.                                 |
| `enableHttpDebugging`  | boolean | `false` | Enable HTTP request/response debugging for the rendering engine.                           |
| `enableMetrics`        | boolean | `false` | Enable metrics collection for rendering operations.                                        |
| `concurrency`          | integer | `8`     | Number of concurrent browser instances for parallel rendering.                             |
| `baseHttpPort`         | integer | `42000` | Base port number for browser instances. Each instance uses baseHttpPort + instance number. |

### log

Configuration for application logging.

| Field              | Type    | Default                                 | Description                                                         |
|--------------------|---------|-----------------------------------------|---------------------------------------------------------------------|
| `level`            | string  | `"info"`                                | Logging level. Options: "debug", "info", "warn", "error", "fatal".  |
| `format`           | string  | `"pretty"`                              | Console output format. Options: "pretty" (human-readable), "json".  |
| `includeTimestamp` | boolean | `true`                                  | Include timestamp in log entries.                                   |
| `includeCaller`    | boolean | `false`                                 | Include caller information (file and line number) in log entries.   |
| `includeHostname`  | boolean | `true`                                  | Include hostname in log entries.                                    |
| `callerSkipFrames` | integer | `2`                                     | Number of stack frames to skip when determining caller information. |
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
    "options": {
      "authTokenSecret": "your-secure-random-token-here"
    }
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
    "options": {
      "authTokenSecret": "your-secure-random-token-here"
    }
  }
}
```

### High-Concurrency Configuration

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
    "baseHttpPort": 42000
  }
}
```

### Development Configuration with Debug Logging

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

1. **Authentication Secret**: Always change `apiServer.options.authTokenSecret` from the default value in production
   environments. Use a strong, randomly generated secret.

2. **TLS Certificates**: When enabling TLS, ensure certificates are properly secured with appropriate file permissions (
   e.g., 0600 for private keys).

3. **TLS Key Passwords**: Prefer using `tlsKeyPasswordEnvVar` or `tlsKeyPasswordFile` over `tlsKeyPassword` to avoid
   storing passwords in the configuration file.

4. **Trusted Proxies**: Only add verified proxy IP addresses to `trustedProxies` to prevent IP spoofing attacks.

5. **File Permissions**: Log files will be created with permissions specified in `log.filePermissions`. The default (
   420 = 0644) makes files readable by all users but writable only by the owner.

## Environment Variables

Configuration values can be overridden using environment variables. The TLS key password specifically supports
`tlsKeyPasswordEnvVar` for secure password management.

## Loading Configuration

The server looks for configuration files in the following locations:

1. Path specified via `-config` command-line flag
2. `config/config.json` in the current directory
3. Default built-in configuration

A sample configuration file is available at `config/config.sample.json`.
