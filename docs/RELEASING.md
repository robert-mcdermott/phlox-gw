# Phlox-GW Release Guide

This guide defines how Phlox-GW versions, binaries, tags, and GitHub Releases are
produced. It covers the manual process available today and the recommended tag-triggered
GitHub Actions process for future releases.

GitHub Releases are based on Git tags. Each Phlox-GW release publishes five compiled
binaries plus a SHA-256 checksum manifest. GitHub also generates source ZIP and tar
archives automatically; those source archives are not substitutes for the platform
binaries.

Authoritative GitHub references:

- [About GitHub Releases](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases)
- [Creating and managing releases](https://docs.github.com/en/repositories/releasing-projects-on-github/managing-releases-in-a-repository)
- [`gh release create`](https://cli.github.com/manual/gh_release_create)

## Release Contents

The repository-root `VERSION` file is the product-version source of truth. A release tag
must match it exactly, including the leading `v`, for example `v0.1.0`.

Each release contains:

| Asset | Platform |
| --- | --- |
| `phlox-gw-darwin-arm64` | macOS on Apple silicon |
| `phlox-gw-linux-amd64` | Linux x86-64 |
| `phlox-gw-linux-arm64` | Linux ARM64 |
| `phlox-gw-windows-amd64.exe` | Windows x86-64 |
| `phlox-gw-windows-arm64.exe` | Windows ARM64 |
| `checksums.txt` | SHA-256 hashes for all five binaries |

The current binaries are not code-signed or notarized. Users downloading raw macOS or Linux binaries may need to make them executable with
`chmod +x`.

## Common Preconditions

Complete these steps regardless of which publishing path is used:

1. Merge the intended release changes into `main`.
2. Confirm all required GitHub Actions jobs pass on the exact release commit.
3. Update `VERSION` in a reviewed commit if the release version is changing.
4. Confirm the Go toolchain matches the exact version declared by `go.mod`.
5. Complete the release-candidate checklist in `docs/RELEASE_PREFLIGHT.md`.
6. Prepare release notes using the checklist below.

Do not release from a worktree with tracked or untracked changes. The release build script
marks tracked changes as `-dirty`, but a separate status check is still required to catch
untracked files:

```bash
git status --porcelain
```

The command must print nothing.

## Path A: Manual Build And Publication

Use this process while no tag-triggered release workflow is enabled, or as a documented
emergency fallback. If the automated workflow described later is installed, pushing the
tag creates the draft release automatically; do not also create a manual release for that
tag.

### 1. Prepare A Clean Release Checkout

```bash
git switch main
git pull --ff-only
git status --porcelain

VERSION="$(tr -d '[:space:]' < VERSION)"
echo "$VERSION"
```

Confirm the status output is empty and the version is the intended semantic version.

### 2. Run Final Quality Checks

```bash
go version
go test ./...
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

npm run build --prefix frontend
git diff --exit-code -- frontend/dist
```

The vulnerability scan must report zero vulnerabilities affecting called code. The
frontend build must leave the checked-in `frontend/dist` assets unchanged.

### 3. Create The Local Release Tag

Use a signed tag when signing is configured:

```bash
git tag -s "$VERSION" -m "Phlox-GW $VERSION"
```

Otherwise, create an annotated tag:

```bash
git tag -a "$VERSION" -m "Phlox-GW $VERSION"
```

Do not push the tag until the build and local verification succeed. If a problem is found
before the tag is pushed, correct the release commit and recreate the local tag at the new
commit.

### 4. Build And Verify The Assets

```bash
scripts/build-release.sh --clean
```

On Windows PowerShell, use:

```powershell
scripts\build-release.ps1 -Clean
```

On macOS, verify all generated checksums:

```bash
cd dist
shasum -a 256 -c checksums.txt
cd ..
```

On Linux:

```bash
cd dist
sha256sum --check checksums.txt
cd ..
```

Run the native artifact and inspect the embedded identity:

```bash
./dist/phlox-gw-darwin-arm64 --version
```

The output must contain the intended version and release commit, and must not contain a
`-dirty` suffix. Smoke-test other artifacts on their target operating systems when those
systems are available.

### 5. Push The Tag

```bash
git push origin "$VERSION"
```

Confirm the pushed tag resolves to the same commit that produced the assets.

### 6. Create A Draft GitHub Release

Through the GitHub web interface:

1. Open the repository's **Releases** page and select **Draft a new release**.
2. Choose the existing tag matching `VERSION`.
3. Set the title to `Phlox-GW <version>`.
4. Generate or enter release notes, then add the required operational details below.
5. Upload all five binaries and `checksums.txt`.
6. Save the release as a draft. Do not publish it yet.

Alternatively, authenticate GitHub CLI and create the draft from the repository root:

```bash
gh auth status

gh release create "$VERSION" \
  dist/phlox-gw-darwin-arm64 \
  dist/phlox-gw-linux-amd64 \
  dist/phlox-gw-linux-arm64 \
  dist/phlox-gw-windows-amd64.exe \
  dist/phlox-gw-windows-arm64.exe \
  dist/checksums.txt \
  --verify-tag \
  --draft \
  --title "Phlox-GW $VERSION" \
  --generate-notes
```

If `gh auth status` reports an expired or missing credential, run `gh auth login` before
continuing. `--verify-tag` ensures the CLI refuses to create the release unless the tag is
already present on GitHub.

### 7. Verify The Draft Assets

Download the draft assets to a new directory and verify them rather than relying only on
the original local files:

```bash
CHECK_DIR="$(mktemp -d)"
gh release download "$VERSION" --dir "$CHECK_DIR"

cd "$CHECK_DIR"
shasum -a 256 -c checksums.txt
cd -
```

On Linux use `sha256sum --check checksums.txt`. Confirm all six expected assets are
present, run the downloaded native binary with `--version`, and review the rendered
release notes and download links.

### 8. Publish

Publish from the GitHub web interface after review, or use:

```bash
gh release edit "$VERSION" --draft=false --latest
```

After publication, repeat the download and checksum verification against the public
release. Then update the product website to link to:

```text
https://github.com/robert-mcdermott/phlox-gw/releases/latest
```

Version-specific assets use this URL form:

```text
https://github.com/robert-mcdermott/phlox-gw/releases/download/v0.1.0/phlox-gw-darwin-arm64
```

## Path B: Tag-Triggered GitHub Actions Draft

This is the recommended steady-state process. A maintainer pushes an approved version tag;
GitHub Actions validates the tagged source, builds and verifies all assets, and creates a
**draft** GitHub Release. A human release manager still reviews and publishes the draft.

The workflow deliberately does not publish automatically. This preserves a review point
for release notes, platform smoke-test results, checksums, known limitations, and download
links.

### Operator Process

1. Merge the release commit to `main` and wait for all required CI checks to pass.
2. Confirm `VERSION`, `go.mod`, and the release notes are correct.
3. Create a signed or annotated tag whose name exactly matches `VERSION`.
4. Push only that tag.
5. Watch the **Release** workflow and investigate any failed validation.
6. Open the draft release created by the workflow.
7. Complete the release notes and target-platform smoke tests.
8. Download and verify the draft assets.
9. Publish the draft explicitly.
10. Verify the public downloads and update the product website.

The operator commands are:

```bash
git switch main
git pull --ff-only
test -z "$(git status --porcelain)"

VERSION="$(tr -d '[:space:]' < VERSION)"
git tag -s "$VERSION" -m "Phlox-GW $VERSION"  # use -a if signing is unavailable
git push origin "$VERSION"
```

### Proposed `.github/workflows/release.yml`

Add the following workflow when automated release production is enabled:

```yaml
name: Release

on:
  push:
    tags:
      - "v*"

permissions:
  contents: read

concurrency:
  group: release-${{ github.ref }}
  cancel-in-progress: false

jobs:
  release-draft:
    name: Validate, build, and draft release
    runs-on: ubuntu-latest
    timeout-minutes: 45
    permissions:
      contents: write
    env:
      GOVULNCHECK_VERSION: v1.6.0

    steps:
      - name: Checkout tagged source
        uses: actions/checkout@v7
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: true

      - name: Set up Node.js
        uses: actions/setup-node@v6
        with:
          node-version: "22"

      - name: Verify tag, version, and Go toolchain
        run: |
          version="$(tr -d '[:space:]' < VERSION)"
          required_go="$(awk '$1 == "go" { print $2; exit }' go.mod)"
          actual_go="$(go env GOVERSION)"

          echo "Tag:        ${GITHUB_REF_NAME}"
          echo "Version:    ${version}"
          echo "Required Go: go${required_go}"
          echo "Resolved Go: ${actual_go}"

          [[ "${GITHUB_REF_TYPE}" == "tag" ]]
          [[ "${GITHUB_REF_NAME}" == "${version}" ]]
          [[ "${actual_go}" == "go${required_go}" ]]

      - name: Download Go modules
        run: go mod download

      - name: Check Go formatting
        run: |
          unformatted="$(gofmt -l .)"
          if [[ -n "$unformatted" ]]; then
            echo "The following Go files need formatting:"
            echo "$unformatted"
            exit 1
          fi

      - name: Build frontend
        run: npm run build --prefix frontend

      - name: Check frontend source and dist parity
        run: git diff --exit-code -- frontend/dist

      - name: Vet Go code
        run: go vet ./...

      - name: Run tests
        run: go test ./...

      - name: Run race detector
        run: go test -race ./...

      - name: Install pinned govulncheck
        run: go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}

      - name: Scan for reachable vulnerabilities
        run: |
          "$(go env GOPATH)/bin/govulncheck" ./...

      - name: Build all release targets
        run: scripts/build-release.sh --skip-frontend --clean

      - name: Verify release checksums
        working-directory: dist
        run: sha256sum --check checksums.txt

      - name: Verify native binary identity
        run: |
          expected="phlox-gw ${GITHUB_REF_NAME}"
          actual="$(./dist/phlox-gw-linux-amd64 --version)"
          echo "$actual"
          [[ "$actual" == "$expected"* ]]
          [[ "$actual" != *-dirty* ]]

      - name: Create draft GitHub Release
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          gh release create "${GITHUB_REF_NAME}" \
            dist/phlox-gw-darwin-arm64 \
            dist/phlox-gw-linux-amd64 \
            dist/phlox-gw-linux-arm64 \
            dist/phlox-gw-windows-amd64.exe \
            dist/phlox-gw-windows-arm64.exe \
            dist/checksums.txt \
            --verify-tag \
            --draft \
            --title "Phlox-GW ${GITHUB_REF_NAME}" \
            --generate-notes
```

Before enabling the workflow, apply the repository's chosen GitHub Actions pinning and
update policy. The sample intentionally uses the same official action major versions as
the current CI workflow.

If the final release-creation command partially succeeds, inspect the existing draft and
upload any missing assets or delete the incomplete draft before rerunning. Do not delete,
move, or recreate a tag after a release has been published.

## Release Notes Checklist

Generated release notes are only a starting point. Every public Phlox-GW release should
also state:

- Release highlights and important fixes.
- Supported operating systems and architectures.
- Installation and `--version` examples.
- The first-run random administrator password and mandatory rotation behavior.
- Upgrade steps and the instruction to back up SQLite or Postgres first.
- Whether database schema changes are backward compatible.
- TLS termination expectations.
- That directly stored provider secrets are not encrypted at rest.
- Whether macOS and Windows binaries are code-signed or notarized.
- Known limitations and deferred release blockers.
- How to verify the downloaded binaries with `checksums.txt`.
- Links to the operator, provider, API-usage, and security documentation.

## Post-Release Verification

After publication:

1. Confirm the release is marked **Latest** unless it is intentionally a prerelease.
2. Confirm the tag resolves to the approved release commit.
3. Download every public asset and verify `checksums.txt`.
4. Smoke-test the native artifacts available to the release team.
5. Confirm the release page and product website links work without authentication.
6. Confirm the website does not link to a nonexistent platform or filename.
7. Record the release URL and final validation evidence in
   `docs/RELEASE_PREFLIGHT.md`.
8. Announce the release only after these checks pass.

For a prerelease such as `v0.2.0-rc.1`, mark the GitHub Release as a prerelease and do not
make it the latest stable release.
