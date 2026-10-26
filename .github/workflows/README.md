# GitHub Actions Workflows

This directory contains GitHub Actions workflows for CI/CD automation.

## Releasing

Push a `v*` tag (the supported path). `release.yml` validates the commit, generates, scans (Grype, fails on high) and
signs the source SBOMs, then builds, scans (Trivy, fails on fixed CRITICAL OS vulnerabilities) and publishes the image,
then creates the release and attaches all SBOMs. Nothing is pushed or released if any of these fails.

### Creating the release in the GitHub UI

With a **new** tag, the UI creates the tag and publishes the release at once, so the release is public before the
scans run; if they fail, delete the release. With an **existing** tag no workflow runs (use "Re-run failed jobs" on the
failed `Release` run instead, or push a new tag). Untick "Generate release notes" in the UI (or leave the notes to the
workflow): the workflow appends generated notes to an existing release, which duplicates them if the UI already
generated some.

## Workflows

### ci.yml - Continuous Integration

Runs on every push and pull request to `development`, `main`, and `master` branches. Token permissions: `contents: read`.

**Jobs:**

1. **lint** - `go vet`, `gofmt` check and `golangci-lint` (pinned version)
2. **test** - Installs the pinned Chrome for Testing build with `go run ./cmd/browser-update`, then runs `make test-integration` (10 minute timeout)
3. **build** - Builds the project with `make build`
4. **govulncheck** - Scans Go dependencies for known vulnerabilities (pinned `govulncheck` version)
5. **build-docker** - Builds the multi-platform (`linux/amd64`, `linux/arm64`) Docker image with QEMU and Buildx, using the GitHub Actions cache
6. **e2e-docker** - End-to-end Docker test as a matrix on native amd64 and arm64 (`ubuntu-24.04-arm`) runners:
   - asserts the image architecture
   - generates a random API key for the run (`openssl rand -hex 32`, masked in the logs) and passes it as `ZIPREPORT_API_KEY`
   - sends a test render request and validates the PDF response
7. **CI Success** - Aggregates the jobs above; this is the check to require in branch protection

### pr.yml - Pull Request

Runs on pull requests (opened, synchronize, reopened). Verifies `go.mod` and `go.sum` are tidy. Token permissions: `contents: read`.

### release.yml - Release

Runs on `v*` tag pushes (and is the single path that publishes a release; see "Releasing").

**Jobs, in order:**

1. **validate** - `go vet`, `gofmt`, `make build`
2. **sbom** - Calls `sbom.yml` with `secrets: inherit` (permissions: `contents: read`, `id-token: write`)
3. **docker** - Calls `docker.yml` (permissions: `contents: read`, `packages: write`, `security-events: write`)
4. **release** - Creates the GitHub release with generated notes (permissions: `contents: write`)
5. **attach_sboms** - Downloads the `sbom-docker-<commit-sha>` and `sbom-source-<commit-sha>` artifacts and attaches
   `bom-cyclonedx.json`, `bom-spdx.json`, `sbom-cyclonedx.json`, `sbom-spdx.json` and the two `.sig` files to the
   release (permissions: `contents: write`)

A called workflow can never hold more permissions than the calling job grants.

### docker.yml - Docker Image Publishing

Builds, scans and publishes Docker images to GitHub Container Registry (ghcr.io). Runs only when called by
`release.yml` for a `v*` tag; nothing else publishes an image.

**Steps of the `docker_publish` job (in order):**

1. Builds the `linux/amd64` image locally (not pushed), tagged `zipreport-server:scan`
2. Scans it with Trivy; the build fails on fixed CRITICAL OS vulnerabilities (unfixed ones are ignored,
   `limit-severities-for-sarif: true` keeps the threshold at CRITICAL)
3. Uploads the Trivy SARIF results to the GitHub Security tab (does not block publication if the upload itself fails)
4. Builds and pushes the multi-platform image (`linux/amd64`, `linux/arm64`) with SBOM and provenance attestations.
   This step runs only if the scan passed. The arm64 image comes from the same Dockerfile and package set, so
   scanning amd64 gates both.
5. Generates image-based SBOMs (CycloneDX and SPDX, `bom-cyclonedx.json` and `bom-spdx.json`) from the pushed image
   and uploads them as the `sbom-docker-<commit-sha>` artifact (retained for 90 days)

**Image Tags:**

- `latest` - The latest non-prerelease version tag
- `<version>` - Release version (e.g., `2.3.0` from `v2.3.0` tag)
- `<major>.<minor>` - Latest patch version (e.g., `2.3`)
- `<major>` - Latest minor version (e.g., `2`)

**Triggers:**
- Called by `release.yml` for `v*` tags (tagged with version number); branch pushes, schedules and manual runs do not
  publish

**Permissions:**
- `contents: read` - Check out the repository
- `packages: write` - Push to GitHub Container Registry
- `security-events: write` - Upload Trivy SARIF results

### sbom.yml - Source SBOMs and Scan

Callable from `release.yml` (it has no other trigger). Permissions: `contents: read`, `id-token: write` (keyless signing), set on the job.

**Steps:** generates a CycloneDX SBOM (`cyclonedx-gomod`) and an SPDX SBOM (Syft) from the source, scans both with
Grype (fails on high severity), signs both with Cosign, and uploads `sbom-cyclonedx.json`, `sbom-spdx.json` and their
`.sig` files as the `sbom-source-<sha>` artifact (`release.yml` attaches them to the release). Tool versions are pinned.

If the `DTRACK_API_KEY` and `DTRACK_HOST` secrets are both set, the CycloneDX SBOM is also uploaded to Dependency-Track.
A job-level `HAS_DTRACK` flag (`true`/`false`, computed from the secrets, never their values) gates that step, and the
two secrets are exposed only to that step's environment. `DTRACK_HOST` must start with `https://`.

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
export ZIPREPORT_API_KEY="$(openssl rand -hex 32)"
docker run -p 6543:6543 -e ZIPREPORT_API_KEY zipreport-server:local

# Or with custom config
docker run -p 6543:6543 -v $(pwd)/config:/app/config zipreport-server:local
```

## Security Scanning

### Vulnerability Scanning

The Docker workflow scans the image with [Trivy](https://trivy.dev/) before it is pushed:
- Scans OS packages
- Fails the build, and publishes nothing, if fixed CRITICAL vulnerabilities are found
- Results are uploaded to the GitHub Security tab (Code scanning alerts)

### Dependency Scanning

The CI workflow runs `govulncheck` to detect known vulnerabilities in Go dependencies at the source level.

## Software Bill of Materials (SBOM)

### SBOM Formats

1. **CycloneDX**
   - Industry-standard SBOM format, includes license information
   - Source (`sbom.yml`): generated from Go modules using `cyclonedx-gomod` (`sbom-cyclonedx.json`)
   - Image (`docker.yml`): generated from the pushed image using Anchore Syft (`bom-cyclonedx.json`)
   - Compatible with dependency-track and other SBOM tools

2. **SPDX**
   - ISO/IEC 5962:2021 standard format
   - Generated using Anchore Syft (`sbom-spdx.json` for source, `bom-spdx.json` for the image)

### Source vs Image SBOMs

- **Source SBOMs** cover Go module dependencies
- **Image SBOMs** cover the full container image including OS packages, Chrome, fonts, and all runtime dependencies

### Accessing SBOMs

**From a GitHub release:**
- `bom-cyclonedx.json` and `bom-spdx.json` (image)
- `sbom-cyclonedx.json`, `sbom-spdx.json` and their `.sig` signatures (source)

**From the Docker workflow run:**
- Download the `sbom-docker-<commit-sha>` artifact

**From the Release workflow run:**
- Download the `sbom-source-<sha>` artifact

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

## Secrets

Besides the default `GITHUB_TOKEN`, only `sbom.yml` uses secrets, both optional: `DTRACK_API_KEY` and `DTRACK_HOST`
(Dependency-Track upload). No other secrets are required.

## Cache Strategy

- **ci.yml** and **docker.yml**: Docker build layers use the GitHub Actions cache backend; Go modules are cached by `actions/setup-go`

## Action Versions

Third-party actions (any owner other than `actions` and `github`) are pinned to a full commit SHA with the tag as a
comment; Dependabot keeps the pins current. `actions/*` and `github/*` stay on tags. Tools installed by the
workflows (`golangci-lint`, `govulncheck`, `cyclonedx-gomod`, Syft, Grype) are pinned to release versions.
A new third-party action must be added SHA-pinned, and a new secret goes on the single step that uses it.

## Troubleshooting

### Test Failures

If tests fail:
1. Check the test output in the Actions tab
2. Run tests locally: `make test-integration`
3. Ensure Go 1.27.3 is installed locally

### Docker Build Failures

If Docker builds fail:
1. Check the build logs in the Actions tab
2. Build locally: `docker build -t test .`
3. Verify Dockerfile syntax and dependencies

### Trivy Scan Failures

If the Trivy scan fails the build (nothing is pushed or released):
1. Check the job log for the specific CVEs
2. Update the base image or affected packages
3. Delete the tag and push it again at the fixed commit ("Re-run failed jobs" only helps when the failure was transient)

### Grype Scan Failures

If the Grype scan of the source SBOMs fails the release (nothing is pushed or released):
1. Check the job log for the specific vulnerabilities
2. Update the affected dependency and commit the fix
3. Delete the tag and push it again at the fixed commit ("Re-run failed jobs" only helps when the failure was transient, e.g. a vulnerability-database download error)

### Permission Issues

If publishing to GHCR fails:
1. Ensure the repository has packages enabled
2. Verify the calling job and `docker.yml` have `packages: write` permission
3. Check the `GITHUB_TOKEN` has correct scopes
