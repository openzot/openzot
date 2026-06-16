# Releasing zot

This document describes how to build, version, and release the `zot` binary.

## Overview

Releases are driven by the **`VERSION` file**. Bumping it on the `main` branch
triggers an automated pipeline that tags the commit and publishes multi-platform
binaries as a GitHub Release:

1. Edit `VERSION` (e.g. `0.1.0` → `0.1.1`) and merge it to `main`.
2. [`tag-release.yaml`](.github/workflows/tag-release.yaml) reads `VERSION` and,
   if the matching `v*` tag does not already exist, creates and pushes it, then
   dispatches the Release workflow.
3. [`release.yaml`](.github/workflows/release.yaml) builds the binary for each
   target platform, packages each into a `.tar.gz` (with `README.md` and
   `zot.example.yaml`), generates SHA-256 checksums, and creates a GitHub
   Release with notes taken from the latest `CHANGELOG.md` section.

You can also release manually by pushing a tag yourself:

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

## Version embedding

The version is baked into the binary at build time via `-ldflags`:

```
-X github.com/chatbotkit/zot/internal/version.Version=<version>
```

The [`Makefile`](Makefile) derives the version from `git describe` for local
builds; the release workflow uses the pushed tag. Built without ldflags (e.g.
`go run`), the version is `dev` and the GitHub update check is skipped.

Check the embedded version with:

```bash
zot --version
```

## Changelog

`CHANGELOG.md` is the source of truth for release notes. Add a new
`## [x.y.z] - YYYY-MM-DD` section at the top before bumping `VERSION`; the
release workflow extracts that latest section as the GitHub Release body.
