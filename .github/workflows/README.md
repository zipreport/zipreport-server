# GitHub Actions Workflows

This directory contains GitHub Actions workflows for CI/CD automation.

## Workflows

### ci.yml - Continuous Integration

Runs on every push and pull request to `development`, `main`, and `master` branches.

**Jobs:**

1. **test** - Runs tests and code quality checks
   - Sets up Go 1.24.7
   - Caches Go modules for faster builds
   - Downloads and verifies dependencies
   - Runs `go fmt` to check code formatting
   - Runs `go vet` for static analysis
   - Builds the project with `make build`
   - Runs tests with `make test-short` (10 minute timeout)
   - Generates Software Bill of Materials (SBOM) in CycloneDX format
   - Generates Software Bill of Materials (SBOM) in SPDX format
   - Uploads SBOM files as artifacts (retained for 90 days)

2. **build-docker** - Builds Docker image (requires test to pass)
   - Sets up Docker Buildx
   - Builds Docker image without pushing
   - Uses GitHub Actions cache for faster builds

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
   - Generates standalone SBOM files in CycloneDX and SPDX formats
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

**Permissions Required:**
- `contents: read` - Read repository contents
- `packages: write` - Push to GitHub Container Registry
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
make test-short
```

### Docker Build

```bash
# Build Docker image
docker build -t zipreport-server:local .

# Run container
docker run -p 6543:6543 -v $(pwd)/config:/app/config zipreport-server:local
```

## Software Bill of Materials (SBOM)

Both workflows generate comprehensive Software Bill of Materials (SBOM) files to provide transparency about dependencies and components.

### SBOM Formats

1. **CycloneDX** (`bom-cyclonedx.json`)
   - Industry-standard SBOM format
   - Includes license information
   - Generated using `CycloneDX/gh-gomod-generate-sbom`
   - Compatible with dependency-track and other SBOM tools

2. **SPDX** (`bom-spdx.json`)
   - ISO/IEC 5962:2021 standard format
   - Generated using Anchore Syft
   - Widely adopted across industries
   - Compatible with government compliance requirements

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

## Upgrading Actions

All actions are pinned to major versions to receive automatic updates:
- `actions/checkout@v4`
- `actions/setup-go@v5`
- `actions/cache@v4`
- `docker/setup-buildx-action@v3`
- `docker/login-action@v3`
- `docker/metadata-action@v5`
- `docker/build-push-action@v6`

## Troubleshooting

### Test Failures

If tests fail:
1. Check the test output in the Actions tab
2. Run tests locally: `make test-short`
3. Ensure Go 1.24.7 is installed locally

### Docker Build Failures

If Docker builds fail:
1. Check the build logs in the Actions tab
2. Build locally: `docker build -t test .`
3. Verify Dockerfile syntax and dependencies

### Permission Issues

If publishing to GHCR fails:
1. Ensure the repository has packages enabled
2. Verify workflow has `packages: write` permission
3. Check the `GITHUB_TOKEN` has correct scopes
