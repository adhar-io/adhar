# Adhar Release Guide

**What this is for:** cutting, verifying, hotfixing and rolling back an Adhar
release. Releases are **fully automated** — a single semver git tag drives the
entire pipeline and no release step is performed by hand.

---

## Contents

1. [What a release produces](#1-what-a-release-produces)
2. [Versioning](#2-versioning)
3. [The automated pipeline](#3-the-automated-pipeline)
4. [Cutting a release](#4-cutting-a-release)
5. [Pre-release checklist](#5-pre-release-checklist)
6. [Verifying a release](#6-verifying-a-release)
7. [Testing locally](#7-testing-locally)
8. [Repository configuration](#8-repository-configuration)
9. [Hotfixes](#9-hotfixes)
10. [Rollback](#10-rollback)

---

## 1. What a release produces

```text
git tag v0.1.0 ──▶ release workflow ──▶ GoReleaser ──▶ ┌ GitHub Release (archives, checksums, SBOMs, signatures, notes)
   (push)                                              ├ ghcr.io/adhar-io/adhar (multi-arch, signed)
                                                       ├ adhar-control-plane-<version>.xpkg
                                                       └ adhar-io/homebrew-tap (brew formula)
```

| Artifact | Detail |
| --- | --- |
| **Binaries** | linux/darwin/windows × amd64/arm64 (no windows/arm64), as `adhar-<version>-<os>-<arch>.tar.gz` (`.zip` on Windows), each bundling `README.md`, `LICENSE` and `docs/` |
| **Checksums** | `checksums.txt` (SHA-256) covering every archive |
| **SBOMs** | one SPDX document per archive (`<artifact>.spdx.sbom.json`), catalogued by syft |
| **Signatures** | cosign **keyless** (Sigstore Fulcio + Rekor) over `checksums.txt`, the SBOMs, and the container images — no long-lived keys; the certificate is bound to the release run's GitHub OIDC identity |
| **Container images** | `ghcr.io/adhar-io/adhar:<version>` (+ `latest` for stable releases) — distroless, non-root, multi-arch manifest for amd64/arm64, from `Dockerfile.goreleaser` |
| **Crossplane control plane** | `adhar-control-plane-<version>.xpkg`, built by a GoReleaser before-hook (`make build-control-plane VERSION={{ .Tag }}`) into the gitignored `platform/controlplane/dist/` and attached via `release.extra_files` — it is never committed |
| **Homebrew formula** | [`adhar-io/homebrew-tap`](https://github.com/adhar-io/homebrew-tap) |
| **Release notes** | generated from commit messages, grouped into Features (`feat:`), Bug Fixes (`fix:`) and Other Changes; `docs:`/`test:`/`chore:` are excluded |

Because commit messages become release notes, use
[Conventional Commits](https://www.conventionalcommits.org) on `main`.
[`.goreleaser.yaml`](../.goreleaser.yaml) is the single source of truth for
artifacts.

## 2. Versioning

Adhar follows [Semantic Versioning](https://semver.org), starting from `v0.1.0`:

| Segment | Bump when… | Example |
| --- | --- | --- |
| **MAJOR** | Breaking API/config changes | `v1.0.0` |
| **MINOR** | New features, new providers, backwards-compatible | `v0.2.0` |
| **PATCH** | Bug fixes, security patches | `v0.1.1` |
| **Prerelease** | Alpha/beta/release-candidate builds | `v0.2.0-rc.1` |

Prerelease tags publish as GitHub **prereleases**; they do not move the `latest`
container tag and do not update the Homebrew formula (`skip_upload: auto`).

**No file in the repository needs a version bump.** The binary's version comes
from the tag via ldflags (`cmd/version.Version`, `cmd/version.GitCommit`,
`cmd/version.BuildDate`), and the Makefile's `VERSION` is derived from
`git describe --tags` — which also names the control-plane `.xpkg`, so the
binary and the package always stamp the same version. Only the `README.md`
version badge is maintained by hand.

## 3. The automated pipeline

The `release` workflow ([`.github/workflows/release.yaml`](../.github/workflows/release.yaml))
triggers on tags matching `v[0-9]+.[0-9]+.[0-9]+` (and `-*` prerelease
suffixes), or manually via `workflow_dispatch`. It:

1. Checks out full history (`fetch-depth: 0`) so GoReleaser can compute the changelog
2. For manual runs: validates the version input, creates and pushes the tag
3. Sets up Go (from `go.mod`) and verifies `make build` succeeds, then
   `git checkout -- .` to discard regenerated tracked files — GoReleaser refuses
   to release from a dirty tree
4. Sets up QEMU + Buildx and logs in to `ghcr.io` (built-in `GITHUB_TOKEN`)
5. Mints a Homebrew tap token via GitHub App (skipped if not configured)
6. Installs **syft** (SBOMs) and **cosign** (keyless signing)
7. Installs GoReleaser **pinned to v2.8.2** — the version the config is
   validated against locally. `latest` has broken releases before via removed
   config fields; bump it deliberately
8. Runs `goreleaser release --clean`

The job needs `contents: write`, `packages: write` and `id-token: write` (the
last one mints the OIDC token Fulcio binds the signing certificate to).

## 4. Cutting a release

### Option A — tag from the command line

```bash
git checkout main && git pull origin main
make release v0.2.0
```

`make release` takes the new version as a **positional argument**
(`VERSION=v0.2.0` also works as an explicit override). It refuses an existing
tag and a malformed version, then creates an annotated tag and pushes it. The
workflow does the rest — watch it at
`https://github.com/adhar-io/adhar/actions/workflows/release.yaml`.

### Option B — from the GitHub UI

**Actions → release → Run workflow**, enter the version (e.g. `v0.2.0`). The
workflow validates the format, creates the tag, and publishes the release in the
same run.

## 5. Pre-release checklist

- [ ] CI green on `main` (tests, lint, e2e)
- [ ] `make lint` passes — it runs golangci-lint **and** the package-contract
      validator (`hack/validate-packages.sh`), which enforces an
      `adhar-package.yaml` on every package
- [ ] `CHANGELOG.md` updated for the new version
- [ ] `README.md` version badge updated
- [ ] Docs updated for new features / breaking changes
- [ ] No known critical security vulnerabilities (code-scanner workflow)
- [ ] `make release-snapshot` succeeds locally

## 6. Verifying a release

After the workflow completes:

```bash
# Archives, checksums, SBOMs and signatures are all on the release page
docker run ghcr.io/adhar-io/adhar:<version> version
brew tap adhar-io/tap && brew install adhar && adhar version

# Verify the signed checksums (keyless — no key to distribute)
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp 'https://github.com/adhar-io/adhar/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Also confirm `adhar-control-plane-<version>.xpkg` is attached to the release.

## 7. Testing locally

```bash
# Full release build without tagging or publishing
make release-snapshot          # skips docker (needs a registry) and sbom (needs syft);
ls dist/                       # signing is skipped by GoReleaser in snapshot mode

# Validate .goreleaser.yaml after editing it
bin/goreleaser check
```

`bin/goreleaser` is installed by `make goreleaser` into the repo-local `bin/`.

## 8. Repository configuration

Binaries, release notes, SBOMs, signatures and GHCR images need **no
configuration** — the workflow's built-in `GITHUB_TOKEN` plus `id-token: write`
cover them.

Homebrew publishing requires a GitHub App with write access to
`adhar-io/homebrew-tap`:

| Setting | Type | Purpose |
| --- | --- | --- |
| `ADHAR_HOMEBREW_APP_ID` | Repository **variable** | App ID used to mint a tap-scoped installation token |
| `ADHAR_HOMEBREW_PRIVATE_KEY` | Repository **secret** | The App's private key (PEM) |

When absent (e.g. on forks), the release still succeeds and only the formula
update is skipped.

## 9. Hotfixes

For a critical bug or vulnerability in the latest release:

```bash
# 1. Branch from the affected tag
git checkout -b hotfix/v0.1.1 v0.1.0

# 2. Fix, test, update CHANGELOG.md
make test && make lint

# 3. Merge back to main via PR, then release from main
make release v0.1.1
```

If security-related: update `SECURITY.md`, create a GitHub security advisory,
and announce it.

## 10. Rollback

A bad release is rolled back by pointing users at the previous version — **never
delete or re-tag a published release** (signatures and SBOMs are bound to the
artifacts that were published).

```bash
# Users: downgrade the binary
curl -fsSL https://github.com/adhar-io/adhar/releases/download/v0.1.0/adhar-0.1.0-linux-amd64.tar.gz | tar xz

# Maintainers: mark the bad release as prerelease/draft on GitHub,
# then ship a fixed patch release
make release v0.1.2
```

- [ ] Investigate the root cause and document it
- [ ] Add a regression test before the next release

> Operators on a released cluster roll the *platform* back with the previous
> binary's `adhar upgrade` plus a Git revert — see
> [PRODUCTION §9](PRODUCTION.md#9-upgrades).

---

**Related**: [GoReleaser config](../.goreleaser.yaml) ·
[Release workflow](../.github/workflows/release.yaml) ·
[Contributing](../CONTRIBUTING.md) · [Changelog](../CHANGELOG.md) ·
[Production Guide](PRODUCTION.md)
