# Adhar Platform Release Guide

**Version**: v0.1.0
**Last Updated**: July 2026

---

## 📋 Table of Contents

1. [Overview](#overview)
2. [Versioning](#versioning)
3. [The Automated Pipeline](#the-automated-pipeline)
4. [Cutting a Release](#cutting-a-release)
5. [Pre-Release Checklist](#pre-release-checklist)
6. [Testing a Release Locally](#testing-a-release-locally)
7. [Repository Configuration](#repository-configuration)
8. [Hotfix Process](#hotfix-process)
9. [Rollback Procedures](#rollback-procedures)

---

## Overview

Adhar releases are **fully automated**. A single semver git tag drives the entire
pipeline: [GoReleaser v2](https://goreleaser.com) (configured in
[`.goreleaser.yaml`](../.goreleaser.yaml)) runs inside the
[`release` GitHub Actions workflow](../.github/workflows/release.yaml) and
builds, packages, and publishes every artifact. No release step is performed by
hand.

```
git tag v0.1.0 ──▶ release workflow ──▶ GoReleaser ──▶ ┌ GitHub Release (binaries, checksums, notes)
   (push)                                              ├ ghcr.io/adhar-io/adhar (multi-arch images)
                                                       └ adhar-io/homebrew-tap (brew formula)
```

## Versioning

Adhar follows [Semantic Versioning](https://semver.org), starting from `v0.1.0`:

| Segment | Bump when... | Example |
|---------|--------------|---------|
| **MAJOR** | Breaking API/config changes | `v1.0.0` |
| **MINOR** | New features, new providers, backwards-compatible | `v0.2.0` |
| **PATCH** | Bug fixes, security patches | `v0.1.1` |
| **Prerelease** | Alpha/beta/release-candidate builds | `v0.2.0-rc.1` |

Prerelease tags are published as GitHub **prereleases**; they do not move the
`latest` container tag and do not update the Homebrew formula (`skip_upload: auto`).

The version embedded in the binary comes from the tag via ldflags
(`cmd/version.Version`, `cmd/version.GitCommit`, `cmd/version.BuildDate`) — no
file in the repository needs a version bump for a release, though the version
badge in `README.md` and the default `VERSION` in the `Makefile` should be kept
current as part of normal maintenance.

## The Automated Pipeline

The `release` workflow triggers on tags matching `v[0-9]+.[0-9]+.[0-9]+` (and
`-*` prerelease suffixes), or manually via `workflow_dispatch`. It:

1. Checks out the full history (`fetch-depth: 0`) so GoReleaser can compute the changelog
2. For manual runs: validates the version input, creates and pushes the tag
3. Sets up Go (from `go.mod`), verifies `make build` succeeds
4. Sets up QEMU + Buildx and logs in to `ghcr.io` (using the built-in `GITHUB_TOKEN`)
5. Mints a Homebrew tap token via GitHub App (skipped if not configured)
6. Runs `goreleaser release --clean`, which publishes:
   - **Binaries** for linux/darwin/windows × amd64/arm64 (no windows/arm64), as `tar.gz` (`zip` on Windows) with `checksums.txt` (SHA-256)
   - **GitHub Release** with notes generated from commit messages, grouped into Features (`feat:`), Bug Fixes (`fix:`), and Other Changes; `docs:`/`test:`/`chore:` commits are excluded
   - **Container images** `ghcr.io/adhar-io/adhar:<version>` (+ `latest` for stable releases) — distroless, non-root, multi-arch manifest for amd64/arm64, built from `Dockerfile.goreleaser`
   - **Homebrew formula** in [`adhar-io/homebrew-tap`](https://github.com/adhar-io/homebrew-tap)

Because commit messages become release notes, use
[Conventional Commits](https://www.conventionalcommits.org) (`feat:`, `fix:`,
`docs:`, `chore:`, ...) on `main`.

## Cutting a Release

### Option A — tag from the command line

```bash
git checkout main && git pull origin main
make release VERSION=v0.1.0
```

The `release` target refuses existing tags, then creates an annotated tag and
pushes it. The workflow does the rest — watch it at
`https://github.com/adhar-io/adhar/actions/workflows/release.yaml`.

### Option B — from the GitHub UI

**Actions → release → Run workflow**, enter the version (e.g. `v0.1.0`).
The workflow validates the format, creates the tag, and publishes the release in
the same run.

### After the workflow completes

- Verify the [release page](https://github.com/adhar-io/adhar/releases) lists all archives + `checksums.txt`
- `docker run ghcr.io/adhar-io/adhar:<version> version`
- `brew tap adhar-io/tap && brew install adhar && adhar version`

## Pre-Release Checklist

- [ ] CI green on `main` (tests, lint, e2e)
- [ ] `CHANGELOG.md` updated for the new version
- [ ] `README.md` version badge and `Makefile` `VERSION` default updated
- [ ] Docs updated for new features / breaking changes
- [ ] No known critical security vulnerabilities (code-scanner workflow)
- [ ] `make release-snapshot` succeeds locally

## Testing a Release Locally

```bash
# Full release build without tagging or publishing (skips container images)
make release-snapshot
ls dist/

# Validate .goreleaser.yaml after editing it
bin/goreleaser check
```

## Repository Configuration

Binaries, release notes, and GHCR images need **no configuration** — the
workflow's built-in `GITHUB_TOKEN` has `contents: write` and `packages: write`.

Homebrew publishing requires a GitHub App with write access to
`adhar-io/homebrew-tap`:

| Setting | Type | Purpose |
|---------|------|---------|
| `ADHAR_HOMEBREW_APP_ID` | Repository **variable** | App ID used to mint a tap-scoped installation token |
| `ADHAR_HOMEBREW_PRIVATE_KEY` | Repository **secret** | The App's private key (PEM) |

When absent (e.g. on forks), the release still succeeds and only the formula
update is skipped.

## Hotfix Process

For a critical bug or vulnerability in the latest release:

```bash
# 1. Branch from the affected tag
git checkout -b hotfix/v0.1.1 v0.1.0

# 2. Fix, test, update CHANGELOG.md
make test && make lint

# 3. Merge back to main via PR, then release from main
make release VERSION=v0.1.1
```

If security-related: update `SECURITY.md`, create a GitHub security advisory,
and announce on Slack.

## Rollback Procedures

A bad release is rolled back by pointing users at the previous version — never
delete or re-tag a published release.

```bash
# Users: downgrade the binary
curl -fsSL https://github.com/adhar-io/adhar/releases/download/v0.1.0/adhar-0.1.0-linux-amd64.tar.gz | tar xz

# Maintainers: mark the bad release as prerelease/draft on GitHub,
# then ship a fixed patch release
make release VERSION=v0.1.2
```

- [ ] Investigate root cause and document it
- [ ] Add a regression test before the next release

---

## Additional Resources

- **[GoReleaser configuration](../.goreleaser.yaml)** — the single source of truth for artifacts
- **[Release workflow](../.github/workflows/release.yaml)** — CI automation
- **[Contributing Guide](../CONTRIBUTING.md)** — commit conventions
- **[Changelog](../CHANGELOG.md)** — version history

**Happy Releasing! 🚀**
