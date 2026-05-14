# GitHub Actions Workflows

This directory contains GitHub Actions workflows for CI/CD automation.

## Workflows

### ci.yml - Continuous Integration

Runs on every push and pull request to `development`, `main`, and `master` branches.

**Jobs:**

1. **test** - Runs tests and code quality checks
   - Sets up Go 1.26.3
   - Downloads and verifies dependencies
   - Runs `go fmt` to check code formatting
   - Runs `go vet` for static analysis
   - Runs `golangci-lint` (v2) for comprehensive linting
   - Runs `govulncheck` for vulnerability scanning
   - Builds the project with `make build`
   - Runs tests with `make test-integration` (10 minute timeout)
   - Generates Software Bill of Materials (SBOM) in CycloneDX and SPDX formats
   - Uploads SBOM files as artifacts (retained for 90 days)

2. **build-docker** - Builds multi-platform Docker image (requires test to pass)
   - Sets up QEMU for cross-platform builds
   - Sets up Docker Buildx
   - Builds for `linux/amd64` and `linux/arm64` platforms
   - Uses GitHub Actions cache for faster builds

3. **e2e-docker** - End-to-end Docker test (requires build-docker to pass)
   - Builds and runs Docker container
   - Waits for container to be ready
   - Sends a test render request
   - Validates PDF response

**Triggers:**
- Push to `development`, `main`, or `master` branches
- Pull requests targeting `development`, `main`, or `master` branches

### docker.yml - Docker Image Publishing

Builds and publishes Docker images to GitHub Container Registry (ghcr.io).

**Jobs:**

1. **docker_publish** - Builds and pushes multi-platform Docker images
   - Builds for `linux/amd64` and `linux/arm64` platforms
   - Pushes to `ghcr.io/<owner>/<repo>`
   - Uses GitHub Actions cache for layer caching
   - Generates semantic version tags
   - Generates and attaches SBOM and provenance attestations to images
   - Scans the image with Trivy for CRITICAL vulnerabilities (fails the build if found)
   - Uploads Trivy SARIF results to the GitHub Security tab
   - Generates image-based SBOM files in CycloneDX and SPDX formats (includes OS packages and runtime dependencies)
   - Uploads SBOM files as artifacts (retained for 90 days)
   - Attaches SBOM files to GitHub releases automatically

**Image Tags:**

- `latest` - Latest commit on default branch
- `<branch>` - Latest commit on a specific branch
- `<version>` - Release version (e.g., `2.3.0` from `v2.3.0` tag)
- `<major>.<minor>` - Latest patch version (e.g., `2.3`)
- `<major>` - Latest minor version (e.g., `2`)

**Triggers:**
- Push to `main` or `master` branches (tagged as `latest`)
- GitHub releases (tagged with version number)
- Weekly schedule (Mondays at 06:00 UTC) to pick up base image and dependency patches
- Manual trigger via `workflow_dispatch`

**Permissions Required:**
- `contents: write` - Read repository contents and attach SBOMs to releases
- `packages: write` - Push to GitHub Container Registry
- `security-events: write` - Upload Trivy SARIF results
- `GITHUB_TOKEN` - Automatically provided by GitHub Actions

## Running Workflows Locally

### Test Workflow

```bash
# Format code
make fmt

# Run static analysis
go vet ./...

# Build project
make build

# Run tests
make test-integration
```

### Docker Build

```bash
# Build Docker image
docker build -t zipreport-server:local .

# Run container with API key
docker run -p 6543:6543 -e ZIPREPORT_API_KEY=your-secret-key zipreport-server:local

# Or with custom config
docker run -p 6543:6543 -v $(pwd)/config:/app/config zipreport-server:local
```

## Security Scanning

### Vulnerability Scanning

The Docker workflow scans published images using [Trivy](https://trivy.dev/):
- Scans for OS package and language-specific vulnerabilities
- Fails the build if CRITICAL severity vulnerabilities are found
- Results are uploaded to the GitHub Security tab (Code scanning alerts)
- Weekly rebuilds ensure base image patches are picked up automatically

### Dependency Scanning

The CI workflow runs `govulncheck` to detect known vulnerabilities in Go dependencies at the source level.

## Software Bill of Materials (SBOM)

Both workflows generate Software Bill of Materials (SBOM) files.

### SBOM Formats

1. **CycloneDX** (`bom-cyclonedx.json`)
   - Industry-standard SBOM format
   - Includes license information
   - CI: generated from Go modules using `cyclonedx-gomod`
   - Docker: generated from the built image using Anchore Syft
   - Compatible with dependency-track and other SBOM tools

2. **SPDX** (`bom-spdx.json`)
   - ISO/IEC 5962:2021 standard format
   - Generated using Anchore Syft
   - Widely adopted across industries
   - Compatible with government compliance requirements

### CI vs Docker SBOMs

- **CI SBOMs** cover Go module dependencies (source-level)
- **Docker SBOMs** cover the full container image including OS packages, Chrome, fonts, and all runtime dependencies

### Accessing SBOMs

**From CI Workflow:**
- Navigate to the workflow run in the Actions tab
- Download the `sbom-files` artifact
- Contains both CycloneDX and SPDX formats

**From Docker Workflow:**
- Download the `sbom-docker-<commit-sha>` artifact from the workflow run
- For releases: SBOM files are automatically attached to the GitHub release

**From Docker Images:**
- Docker images include embedded SBOM and provenance attestations
- Inspect with: `docker buildx imagetools inspect ghcr.io/<owner>/<repo>:tag --format "{{ json .SBOM }}"`
- Verify provenance with: `docker buildx imagetools inspect ghcr.io/<owner>/<repo>:tag --format "{{ json .Provenance }}"`

### Using SBOMs

```bash
# Analyze with dependency-track (CycloneDX)
curl -X POST "https://dependency-track.example.com/api/v1/bom" \
  -H "X-Api-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d @bom-cyclonedx.json

# Analyze with Grype (SPDX)
grype sbom:./bom-spdx.json

# Convert between formats
cyclonedx-cli convert --input-file bom-cyclonedx.json --output-format spdxjson
```

## Environment Variables

No environment variables or secrets are required beyond the default `GITHUB_TOKEN` provided by GitHub Actions.

## Cache Strategy

Both workflows use GitHub Actions cache:
- **ci.yml**: Caches Go modules (`~/.cache/go-build`, `~/go/pkg/mod`)
- **docker.yml**: Caches Docker build layers (GitHub Actions cache backend)

## Action Versions

Actions are pinned to Node.js 24-compatible versions:
- `actions/checkout@v6`
- `actions/setup-go@v6`
- `actions/upload-artifact@v6`
- `docker/setup-buildx-action@v4`
- `docker/setup-qemu-action@v4`
- `docker/login-action@v4`
- `docker/metadata-action@v6`
- `docker/build-push-action@v7`
- `golangci/golangci-lint-action@v9`
- `aquasecurity/trivy-action@v0.36.0`
- `anchore/sbom-action@v0.24.0`
- `github/codeql-action/upload-sarif@v4`
- `softprops/action-gh-release@v3`

## Troubleshooting

### Test Failures

If tests fail:
1. Check the test output in the Actions tab
2. Run tests locally: `make test-integration`
3. Ensure Go 1.26.3 is installed locally

### Docker Build Failures

If Docker builds fail:
1. Check the build logs in the Actions tab
2. Build locally: `docker build -t test .`
3. Verify Dockerfile syntax and dependencies

### Trivy Scan Failures

If the Trivy scan fails the build:
1. Check the GitHub Security tab for the specific CVEs
2. Update the base image or affected packages
3. Trigger a manual rebuild via `workflow_dispatch` after fixes

### Permission Issues

If publishing to GHCR fails:
1. Ensure the repository has packages enabled
2. Verify workflow has `packages: write` permission
3. Check the `GITHUB_TOKEN` has correct scopes
