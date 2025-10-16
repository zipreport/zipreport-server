# Integration Tests

This directory contains comprehensive integration tests for the zipreport-server.

## Test Structure

- `integration_test.go` - Core API endpoint tests
- `security_test.go` - Security and path traversal tests
- `fixtures/` - Test data including sample ZIP files

## Running Tests

### Run all tests
```bash
make test
```

### Run with longer timeout (for full integration tests)
```bash
make test-integration
```

### Run quick tests only (skip long-running tests)
```bash
make test-short
```

### Run from command line
```bash
# All tests
go test -v ./test/...

# Short tests only
go test -v -short ./test/...

# With timeout
go test -v -timeout=5m ./test/...
```

## Test Coverage

### Integration Tests (`integration_test.go`)

1. **TestRenderEndpoint_Success** - Tests successful PDF generation
2. **TestRenderEndpoint_MissingAuth** - Tests authentication requirement
3. **TestRenderEndpoint_InvalidAuth** - Tests invalid token handling
4. **TestRenderEndpoint_MissingFile** - Tests missing report file validation
5. **TestRenderEndpoint_InvalidPageSize** - Tests page size validation
6. **TestRenderEndpoint_InvalidMarginStyle** - Tests margin style validation
7. **TestRenderEndpoint_NegativeMargins** - Tests negative margin rejection
8. **TestRenderEndpoint_CustomMargins** - Tests custom margin values
9. **TestRenderEndpoint_LandscapeOrientation** - Tests landscape mode
10. **TestRenderEndpoint_NestedFile** - Tests subdirectory file access
11. **TestRenderEndpoint_DifferentPageSizes** - Tests all supported page sizes (A3, A4, A5, Letter, Legal, Tabloid)
12. **TestRenderEndpoint_BooleanParsing** - Tests various boolean value formats

### Security Tests (`security_test.go`)

1. **TestZptServer_PathTraversal** - Tests path traversal protection with multiple attack vectors:
   - Parent directory traversal (`/../etc/passwd`)
   - Double parent traversal (`/../../etc/passwd`)
   - Middle path traversal (`/test/../../../etc/passwd`)
   - URL encoded traversal
   - Backslash traversal
   - Double slash normalization

2. **TestZptReader_LargeFileProtection** - Tests file reading limits
3. **TestRenderTimeout** - Tests timeout parameter handling
4. **TestConcurrentRequests** - Tests concurrent request handling
5. **TestSSLErrorsOption** - Tests SSL error ignore option

## Test Fixtures

The `fixtures/` directory contains:
- `test.html` - Simple HTML test file
- `subdirectory/nested.html` - Nested file for path testing
- `test.zpt` - ZIP archive containing the test files

## Notes

- Tests use a shared Prometheus metrics instance to avoid duplicate registration errors
- The render engine requires browser automation (Rod/Chromium), so tests may take several seconds to run
- Short mode (`-short` flag) skips timeout and concurrency tests
- Tests automatically clean up resources (browsers, HTTP servers) after completion

## Authentication

All API tests use the test authentication token defined in the test setup. The token is passed via the `X-Auth-Key` header.

## Troubleshooting

### Tests timeout
- Increase timeout: `go test -timeout=10m ./test/...`
- Run in short mode: `go test -short ./test/...`

### Browser/Chromium issues
- Ensure Chrome/Chromium is installed
- Check Rod can download/access browser binaries
- Set `ROD_DEBUG=1` for Rod debugging

### Port conflicts
- Tests use ports 42000+ for temporary HTTP servers
- Ensure these ports are available

## Future Improvements

- Add tests for resource exhaustion protection (file size limits, timeout caps)
- Add tests for zip bomb protection
- Add performance benchmarks
- Add tests for metrics collection
- Add tests for TLS configuration
