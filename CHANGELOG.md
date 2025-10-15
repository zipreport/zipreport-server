# Changelog

All notable changes to ZipReport Server will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

### Security
- Added comprehensive path traversal protection with multiple validation layers
- Implemented proper path cleaning and validation in HTTP file server
- Added bounds checking for custom margin values (negative values rejected)
- Enhanced input validation for page sizes and margin styles
