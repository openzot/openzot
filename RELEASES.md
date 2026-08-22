# Releasing zot

This document describes how to build, version, and release the `zot` binary.

## Overview

Releases are driven by the **`VERSION` file**. Bumping it on the `main` branch
triggers an automated pipeline that tags the commit and publishes multi-platform
binaries as a GitHub Release:

1. Edit `VERSION` (e.g. `0.1.0` → `0.1.1`) and merge it to `main`.
2. CI runs the vulnerability, test, coverage, and build gates for that commit.
3. After CI succeeds, [`tag-release.yaml`](.github/workflows/tag-release.yaml)
   reads `VERSION` from the tested commit and, if the matching `v*` tag does not
   already exist, creates and pushes it, then dispatches the Release workflow.
4. [`release.yaml`](.github/workflows/release.yaml) verifies CI succeeded for
   the exact tagged commit, builds the binary for each target platform,
   packages it into a `.tar.gz` (with `README.md` and `zot.example.yaml`),
   publishes a multi-platform container image, generates
   SHA-256 checksums, and creates a GitHub Release with notes taken from the
   latest `CHANGELOG.md` section and the image coordinates on top.

You can also trigger a release by pushing a tag yourself, but the release will
still refuse to publish unless that exact commit has passed the `main` push CI:

```bash
git tag v0.1.1
git push origin v0.1.1
```

Use [Semantic Versioning](https://semver.org/) with a `v` prefix on tags
(`v1.0.0`, not `1.0.0`). Pre-release versions: `v0.1.0-beta.1`. The `VERSION`
file itself holds the bare version (no `v` prefix); the tag adds it.

### Target platforms

| OS      | Architecture |
| ------- | ------------ |
| Linux   | amd64, arm64 |
| macOS   | amd64, arm64 |
| Windows | amd64        |

### Container images

The same tag also publishes a Linux amd64/arm64 image to
`ghcr.io/openzot/openzot`, built from the repository
[`Dockerfile`](Dockerfile) with provenance attestations and an SBOM. Stable
releases move `latest` and publish `vX.Y.Z`, `X.Y.Z` and `X.Y`; prereleases
(a tag containing `-`) publish only their exact tags and leave `latest` alone.

CI builds and smoke-tests the same Dockerfile on every code push, so a broken
image fails before a tag is ever cut. See [docs/docker.md](docs/docker.md) for
how the image is meant to be run.

## Version embedding

The version is baked into the binary at build time via `-ldflags`:

```
-X github.com/openzot/openzot/internal/version.Version=<version>
```

The [`Makefile`](Makefile) derives the version from the authoritative `VERSION`
file for local builds; the release workflow uses the matching pushed tag. Built without ldflags (e.g.
`go run`), the version is `dev` and the GitHub update check is skipped.

Check the embedded version with:

```bash
zot --version
```

## Local release checks

```bash
make test
make build VERSION=v0.4.1
./zot --version
docker build --build-arg VERSION=v0.4.1 --tag openzot/zot:local .
docker run --rm openzot/zot:local --version
```

## Changelog

`CHANGELOG.md` is the source of truth for release notes. Add a new
`## [x.y.z] - YYYY-MM-DD` section at the top before bumping `VERSION`; the
release workflow extracts that latest section as the GitHub Release body.
